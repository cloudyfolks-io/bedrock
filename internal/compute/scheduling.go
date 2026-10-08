package compute

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
)

var ErrAffinityNamesNamespaces = errors.New("a pod affinity term must not name namespaces or select namespaces")

func ScopedAffinity(in *corev1.Affinity) (*corev1.Affinity, error) {
	if in == nil {
		return nil, nil
	}
	for _, terms := range podTerms(in) {
		for _, term := range terms {
			if len(term.Namespaces) > 0 || term.NamespaceSelector != nil {
				return nil, ErrAffinityNamesNamespaces
			}
		}
	}
	return in.DeepCopy(), nil
}

func podTerms(in *corev1.Affinity) [][]corev1.PodAffinityTerm {
	var groups [][]corev1.PodAffinityTerm
	if in.PodAffinity != nil {
		groups = append(groups, in.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution, weighted(in.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution))
	}
	if in.PodAntiAffinity != nil {
		groups = append(groups, in.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, weighted(in.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution))
	}
	return groups
}

func weighted(terms []corev1.WeightedPodAffinityTerm) []corev1.PodAffinityTerm {
	out := make([]corev1.PodAffinityTerm, 0, len(terms))
	for _, term := range terms {
		out = append(out, term.PodAffinityTerm)
	}
	return out
}
