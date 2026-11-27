package container

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Issue struct{ Field, Message string }

var reservedMountRoots = []string{"/proc", "/sys", "/dev"}

func ValidateContainer(c v1alpha1.Container) []Issue {
	spec := c.Spec
	var issues []Issue
	issues = append(issues, imageIssues(spec.Image)...)
	issues = append(issues, portNameIssues(spec.Ports)...)
	issues = append(issues, resourceIssues(spec.Resources)...)
	issues = append(issues, probeIssues(spec.Probes)...)
	issues = append(issues, mountIssues(spec.Volumes)...)
	return issues
}

func imageIssues(image string) []Issue {
	if strings.ContainsFunc(image, unicode.IsSpace) || strings.HasPrefix(image, "-") {
		return []Issue{{Field: "spec.image", Message: "the image must be an image reference with no spaces"}}
	}
	return nil
}

func portNameIssues(ports []v1alpha1.ContainerPort) []Issue {
	seen := map[string]bool{}
	var issues []Issue
	for index, port := range ports {
		if port.Name == "" {
			continue
		}
		if seen[port.Name] {
			issues = append(issues, Issue{Field: fmt.Sprintf("spec.ports[%d].name", index), Message: fmt.Sprintf("the port name %q is used twice", port.Name)})
		}
		seen[port.Name] = true
	}
	return issues
}

func resourceIssues(resources v1alpha1.ContainerResources) []Issue {
	var issues []Issue
	for field, value := range map[string]*resource.Quantity{
		"spec.resources.requests.cpu":    resources.Requests.CPU,
		"spec.resources.requests.memory": resources.Requests.Memory,
		"spec.resources.limits.cpu":      resources.Limits.CPU,
		"spec.resources.limits.memory":   resources.Limits.Memory,
	} {
		if value != nil && value.Sign() <= 0 {
			issues = append(issues, Issue{Field: field, Message: "the value must be above zero"})
		}
	}
	sortIssues(issues)
	return issues
}

func probeIssues(probes *v1alpha1.ContainerProbes) []Issue {
	if probes == nil {
		return nil
	}
	var issues []Issue
	if probes.Liveness != nil && probes.Liveness.SuccessThreshold != nil && *probes.Liveness.SuccessThreshold != 1 {
		issues = append(issues, Issue{Field: "spec.probes.liveness.successThreshold", Message: "the value must be 1 for a liveness probe"})
	}
	for field, probe := range map[string]*v1alpha1.ContainerProbe{"spec.probes.liveness": probes.Liveness, "spec.probes.readiness": probes.Readiness} {
		if probe != nil && probe.Exec != nil && len(probe.Exec.Command) > 0 && probe.Exec.Command[0] == "" {
			issues = append(issues, Issue{Field: field + ".exec.command", Message: "the first word of the command must not be empty"})
		}
	}
	sortIssues(issues)
	return issues
}

func mountIssues(volumes []v1alpha1.ContainerVolume) []Issue {
	var issues []Issue
	for index, volume := range volumes {
		field := fmt.Sprintf("spec.volumes[%d].mountPath", index)
		clean := path.Clean(volume.MountPath)
		switch {
		case clean != volume.MountPath && clean+"/" != volume.MountPath:
			issues = append(issues, Issue{Field: field, Message: "the mount path must be a clean absolute path"})
		case reservedMount(clean):
			issues = append(issues, Issue{Field: field, Message: "the mount path must not be inside /proc, /sys or /dev"})
		}
	}
	return issues
}

func reservedMount(clean string) bool {
	for _, root := range reservedMountRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

func sortIssues(issues []Issue) {
	slices.SortFunc(issues, func(a, b Issue) int { return strings.Compare(a.Field, b.Field) })
}

func IssueMessage(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}
