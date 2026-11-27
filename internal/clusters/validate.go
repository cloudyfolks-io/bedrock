package clusters

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/k0sversion"
)

type Issue struct{ Field, Message string }

func ValidateCluster(cluster v1alpha1.KubernetesCluster) []Issue {
	var issues []Issue
	if !k0sversion.Valid(cluster.Spec.Version) {
		issues = append(issues, Issue{Field: "spec.version", Message: "the version must look like v1.36.3+k0s.1"})
	}
	issues = append(issues, cidrIssue("spec.network.podCIDR", cluster.Spec.Network.PodCIDR)...)
	issues = append(issues, cidrIssue("spec.network.serviceCIDR", cluster.Spec.Network.ServiceCIDR)...)
	slices.SortFunc(issues, func(a, b Issue) int { return strings.Compare(a.Field, b.Field) })
	return issues
}

func cidrIssue(field, value string) []Issue {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() {
		return []Issue{{Field: field, Message: "the value must be an IPv4 CIDR"}}
	}
	return nil
}

func IssueMessage(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

func overlapMessage(name string, first, second netip.Prefix) string {
	return fmt.Sprintf("the %s %s overlaps %s", name, first, second)
}
