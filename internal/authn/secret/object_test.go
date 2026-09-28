package secret

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSecretObjectLabelsAndOwner(t *testing.T) {
	owner := &v1alpha1.Credential{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"},
		ObjectMeta: metav1.ObjectMeta{Name: "alice-totp", UID: "11111111-1111-1111-1111-111111111111"},
	}
	got := Object(owner, "alice-totp", map[string][]byte{"seed": []byte("seed-bytes")})
	if got.Namespace != "bedrock-system" {
		t.Fatalf("namespace = %q", got.Namespace)
	}
	if got.Labels[v1alpha1.LabelAuthn] != "true" {
		t.Fatalf("label %s = %q", v1alpha1.LabelAuthn, got.Labels[v1alpha1.LabelAuthn])
	}
	if got.Labels[v1alpha1.LabelKind] != "Credential" {
		t.Fatalf("label %s = %q", v1alpha1.LabelKind, got.Labels[v1alpha1.LabelKind])
	}
	if got.Labels[v1alpha1.LabelName] != "alice-totp" {
		t.Fatalf("label %s = %q", v1alpha1.LabelName, got.Labels[v1alpha1.LabelName])
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != owner.UID {
		t.Fatalf("owner references = %+v", got.OwnerReferences)
	}
	if got.OwnerReferences[0].Kind != "Credential" || got.OwnerReferences[0].Controller == nil || !*got.OwnerReferences[0].Controller {
		t.Fatalf("owner reference must be a Credential controller reference, got %+v", got.OwnerReferences[0])
	}
	if string(got.Data["seed"]) != "seed-bytes" {
		t.Fatalf("data = %+v", got.Data)
	}
}
