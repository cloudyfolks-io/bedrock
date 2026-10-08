package tenancy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestWithOwner(t *testing.T) {
	tenant := &v1alpha1.Tenant{ObjectMeta: metav1.ObjectMeta{Name: "acme", UID: types.UID("uid-1")}}
	existing := metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "uid-0"}
	input := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "n", OwnerReferences: []metav1.OwnerReference{existing}}}
	got := WithOwner(input, tenant, v1alpha1.GroupVersion.WithKind("Tenant"))
	if len(got.OwnerReferences) != 2 || got.OwnerReferences[0] != existing {
		t.Fatalf("references = %v", got.OwnerReferences)
	}
	ref := got.OwnerReferences[1]
	if ref.APIVersion != "bedrock.cloudyfolks.io/v1alpha1" || ref.Kind != "Tenant" || ref.Name != "acme" || ref.UID != "uid-1" || ref.Controller == nil || !*ref.Controller {
		t.Fatalf("reference = %+v", ref)
	}
	if len(input.OwnerReferences) != 1 {
		t.Fatalf("input changed: %v", input.OwnerReferences)
	}
}
