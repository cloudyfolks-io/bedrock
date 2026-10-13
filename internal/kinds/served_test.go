package kinds

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
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
