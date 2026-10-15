package kinds

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFirstMissingNamesTheFirstUnservedKind(t *testing.T) {
	served := schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}
	unserved := schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(served, meta.RESTScopeNamespace)

	if _, missing := FirstMissing(mapper, []schema.GroupVersionKind{served}); missing {
		t.Fatal("a served kind was reported missing")
	}
	got, missing := FirstMissing(mapper, []schema.GroupVersionKind{served, unserved})
	if !missing || got != unserved {
		t.Fatalf("got %v %v, want %v", got, missing, unserved)
	}
}

func TestOfGroupListsTheKindsOfTheGroupThatHaveAListKind(t *testing.T) {
	ours := schema.GroupVersion{Group: "ours.test", Version: "v1"}
	theirs := schema.GroupVersion{Group: "theirs.test", Version: "v1"}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(ours.WithKind("Zebra"), &corev1.ConfigMap{})
	scheme.AddKnownTypeWithName(ours.WithKind("ZebraList"), &corev1.ConfigMapList{})
	scheme.AddKnownTypeWithName(ours.WithKind("Ant"), &corev1.ConfigMap{})
	scheme.AddKnownTypeWithName(ours.WithKind("AntList"), &corev1.ConfigMapList{})
	scheme.AddKnownTypeWithName(ours.WithKind("Single"), &corev1.Secret{})
	scheme.AddKnownTypeWithName(theirs.WithKind("Other"), &corev1.ConfigMap{})
	scheme.AddKnownTypeWithName(theirs.WithKind("OtherList"), &corev1.ConfigMapList{})

	got := OfGroup(scheme, "ours.test")
	want := []schema.GroupVersionKind{ours.WithKind("Ant"), ours.WithKind("Zebra")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
