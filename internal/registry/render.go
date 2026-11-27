package registry

import (
	"regexp"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/registry/harbor"
)

const (
	reasonNameInvalid  = "NameInvalid"
	reasonNameReserved = "NameReserved"
	claimSlack         = 5 * time.Second
)

var projectNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,28}[a-z0-9]$`)

func ProjectName(reg *v1alpha1.Registry) string {
	if reg.Spec.Name != "" {
		return reg.Spec.Name
	}
	return reg.Name
}

func nameProblem(name string, rules Rules, approved bool) (string, string) {
	if !projectNamePattern.MatchString(name) {
		return reasonNameInvalid, "the registry name " + name + " must have 3 to 30 lowercase letters, digits or dashes and start and end with a letter or digit"
	}
	if !approved && reservedName(name, rules) {
		return reasonNameReserved, "the registry name " + name + " is reserved for administrators"
	}
	return "", ""
}

func reservedName(name string, rules Rules) bool {
	if slices.Contains(rules.ReservedNames, name) {
		return true
	}
	return slices.ContainsFunc(rules.ReservedPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

func claimed(recordedID, foundID int64, marked *metav1.Time, created time.Time) bool {
	if recordedID != 0 {
		return recordedID == foundID
	}
	return marked != nil && !created.Before(marked.Add(-claimSlack))
}

func approved(obj metav1.Object) bool {
	return obj.GetAnnotations()[AnnotationReservedNameApproved] == "true"
}

func adopting(obj metav1.Object) bool {
	return obj.GetAnnotations()[AnnotationAdopt] == "true"
}

func quotaGB(reg *v1alpha1.Registry, rules Rules) int64 {
	if reg.Spec.QuotaGB != nil {
		return *reg.Spec.QuotaGB
	}
	return rules.DefaultQuotaGB
}

func hasRetention(spec *v1alpha1.RegistryRetention) bool {
	return spec != nil && (spec.KeepLastN != nil || spec.RetainDays != nil)
}

func retentionInput(spec *v1alpha1.RegistryRetention) harbor.RetentionInput {
	if spec == nil {
		return harbor.RetentionInput{}
	}
	input := harbor.RetentionInput{TagPattern: spec.TagPattern, RetainUntagged: spec.RetainUntagged, Schedule: spec.Schedule}
	if spec.KeepLastN != nil {
		value := int(*spec.KeepLastN)
		input.KeepLastN = &value
	}
	if spec.RetainDays != nil {
		value := int(*spec.RetainDays)
		input.RetainDays = &value
	}
	return input
}
