package operator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const (
	autopilotPlanName              = "autopilot"
	planCompleted                  = "Completed"
	controlPlaneProgressAnnotation = "bedrock.cloudyfolks.io/controlplane"
)

var planGVK = schema.GroupVersionKind{Group: "autopilot.k0sproject.io", Version: "v1beta2", Kind: "Plan"}

func runningPlanStates() []string {
	return []string{"", "Schedulable", "SchedulableWait"}
}

func controlPlaneFlow() nodeFlow {
	return nodeFlow{
		Label:      "controlplane",
		Annotation: controlPlaneProgressAnnotation,
		Members:    func(hosts []v1alpha1.Host) []v1alpha1.Host { return hostsWithRole(hosts, v1alpha1.RoleControlPlane) },
		Steps:      func(v1alpha1.Host) []string { return []string{v1alpha1.StepK0sUpdate} },
	}
}

func controlPlane(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	return walkNodes(ctx, env, cluster, controlPlaneFlow())
}

func hostsWithRole(hosts []v1alpha1.Host, role string) []v1alpha1.Host {
	var matching []v1alpha1.Host
	for _, host := range hosts {
		if slices.Contains(host.Spec.Roles, role) {
			matching = append(matching, host)
		}
	}
	return matching
}

func hostsNotAt(hosts []v1alpha1.Host, k0sVersion string) []string {
	var waiting []string
	for _, host := range hosts {
		if host.Status.K0sVersion != k0sVersion {
			waiting = append(waiting, host.Name)
		}
	}
	return waiting
}

func planID(version, stage string, attempt int32) string {
	return fmt.Sprintf("%s-%s-%d", version, stage, attempt)
}

func k0sPlatforms(hosts []v1alpha1.Host, target v1alpha1.Release, arches []string) (map[string]any, string) {
	platforms := map[string]any{}
	for _, arch := range arches {
		url, ok := depotURL(hosts, target.Spec.Version, arch)
		if !ok {
			return nil, fmt.Sprintf("no host serves %s for %s", target.Spec.Version, arch)
		}
		checksum, ok := target.Spec.K0sChecksums[arch]
		if !ok {
			return nil, fmt.Sprintf("Release/%s has no k0s checksum for %s", target.Name, arch)
		}
		platforms["linux-"+arch] = map[string]any{"url": url + "/k0s/k0s", "sha256": strings.TrimPrefix(checksum, "sha256:")}
	}
	return platforms, ""
}

func autopilotPlan(id, version string, platforms map[string]any, controllers, workers []string, concurrent int64, timestamp string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": planGVK.GroupVersion().String(),
		"kind":       planGVK.Kind,
		"metadata":   map[string]any{"name": autopilotPlanName},
		"spec": map[string]any{
			"id":        id,
			"timestamp": timestamp,
			"commands": []any{map[string]any{"k0supdate": map[string]any{
				"version":   version,
				"platforms": platforms,
				"targets":   map[string]any{"controllers": planTarget(controllers, 1), "workers": planTarget(workers, concurrent)},
			}}},
		},
	}}
}

func planTarget(nodes []string, concurrent int64) map[string]any {
	listed := make([]any, 0, len(nodes))
	for _, node := range nodes {
		listed = append(listed, node)
	}
	return map[string]any{"discovery": map[string]any{"static": map[string]any{"nodes": listed}}, "limits": map[string]any{"concurrent": concurrent}}
}

func planIDOf(plan *unstructured.Unstructured) string {
	id, _, _ := unstructured.NestedString(plan.Object, "spec", "id")
	return id
}

func ensurePlan(ctx context.Context, c client.Client, want *unstructured.Unstructured) (*unstructured.Unstructured, string, error) {
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(planGVK)
	err := c.Get(ctx, client.ObjectKey{Name: autopilotPlanName}, current)
	if errors.IsNotFound(err) {
		return nil, "creating autopilot plan " + planIDOf(want), client.IgnoreAlreadyExists(c.Create(ctx, want.DeepCopy()))
	}
	if err != nil {
		return nil, "", err
	}
	if planIDOf(current) == planIDOf(want) {
		return current, "", nil
	}
	return nil, "replacing autopilot plan " + planIDOf(current), client.IgnoreNotFound(c.Delete(ctx, current))
}

func planProgress(label string, plan *unstructured.Unstructured) phaseResult {
	state, _, _ := unstructured.NestedString(plan.Object, "status", "state")
	switch {
	case state == planCompleted:
		return phaseResult{Done: true}
	case slices.Contains(runningPlanStates(), state):
		return phaseResult{Message: fmt.Sprintf("%s: autopilot plan %s is %s%s", label, planIDOf(plan), stateName(state), planTargetStates(plan))}
	}
	return phaseResult{Failure: fmt.Sprintf("%s: autopilot plan %s is %s%s", label, planIDOf(plan), state, planTargetStates(plan))}
}

func stateName(state string) string {
	if state == "" {
		return "new"
	}
	return state
}

func planTargetStates(plan *unstructured.Unstructured) string {
	commands, _, _ := unstructured.NestedSlice(plan.Object, "status", "commands")
	var targets []string
	for _, command := range commands {
		fields, _ := command.(map[string]any)
		for _, kind := range []string{"controllers", "workers"} {
			entries, _, _ := unstructured.NestedSlice(fields, "k0supdate", kind)
			for _, entry := range entries {
				target, _ := entry.(map[string]any)
				targets = append(targets, fmt.Sprintf("%v %v", target["name"], target["state"]))
			}
		}
	}
	if len(targets) == 0 {
		return ""
	}
	return " (" + strings.Join(targets, ", ") + ")"
}
