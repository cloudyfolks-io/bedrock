package policy

import (
	"slices"
	"sort"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func EffectiveGroups(user v1alpha1.User, groups []v1alpha1.Group) []string {
	set := map[string]struct{}{}
	for _, name := range user.Spec.Groups {
		set[name] = struct{}{}
	}
	for _, g := range groups {
		if slices.Contains(g.Spec.Members, user.Name) {
			set[g.Name] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func SecondFactorRequired(s Settings, user v1alpha1.User, groups []v1alpha1.Group, client v1alpha1.OAuthClient) bool {
	if s.RequireSecondFactor || client.Spec.RequireSecondFactor {
		return true
	}
	effective := EffectiveGroups(user, groups)
	for _, g := range groups {
		if g.Spec.RequireSecondFactor && slices.Contains(effective, g.Name) {
			return true
		}
	}
	return false
}

func SecondFactorSatisfied(amr []string) bool {
	return slices.Contains(amr, "otp") || slices.Contains(amr, "mfa") || slices.Contains(amr, "hwk")
}

func MapGroups(mapping []v1alpha1.GroupMapping, external []string) []string {
	byExternal := make(map[string]string, len(mapping))
	for _, m := range mapping {
		byExternal[m.External] = m.Group
	}
	set := map[string]struct{}{}
	for _, name := range external {
		if group, ok := byExternal[name]; ok {
			set[group] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
