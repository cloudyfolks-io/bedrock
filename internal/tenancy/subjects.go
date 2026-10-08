package tenancy

import (
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func Subject(kind, name string) (rbacv1.Subject, bool) {
	known := kind == v1alpha1.SubjectKindUser || kind == v1alpha1.SubjectKindGroup
	if !known || name == "" {
		return rbacv1.Subject{}, false
	}
	return rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: kind, Name: v1alpha1.AuthnPrefix + name}, true
}

func SortedSubjects(subjects []rbacv1.Subject) []rbacv1.Subject {
	sorted := slices.Clone(subjects)
	slices.SortFunc(sorted, func(a, b rbacv1.Subject) int {
		if byKind := strings.Compare(a.Kind, b.Kind); byKind != 0 {
			return byKind
		}
		return strings.Compare(a.Name, b.Name)
	})
	return slices.Compact(sorted)
}
