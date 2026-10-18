package network

import (
	"cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CompareAge(a, b metav1.Object) int {
	return cmp.Or(
		a.GetCreationTimestamp().Time.Compare(b.GetCreationTimestamp().Time),
		cmp.Compare(a.GetNamespace(), b.GetNamespace()),
		cmp.Compare(a.GetName(), b.GetName()),
	)
}

func Older(a, b metav1.Object) bool {
	return CompareAge(a, b) < 0
}
