package compute

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func reconcileSSHKey(t *testing.T, c client.Client, key client.Object) v1alpha1.SSHKey {
	t.Helper()
	r := &SSHKeyReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(key)}); err != nil {
		t.Fatal(err)
	}
	var out v1alpha1.SSHKey
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(key), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func newSSHKey(t *testing.T, c client.Client, publicKey string) *v1alpha1.SSHKey {
	t.Helper()
	ns := uniqueNamespace(t, c)
	key := &v1alpha1.SSHKey{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "alice"},
		Spec:       v1alpha1.SSHKeySpec{PublicKey: publicKey},
	}
	if err := c.Create(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSSHKeyReadyWhenValid(t *testing.T) {
	c, _ := startEnv(t)
	key := newSSHKey(t, c, ed25519Key(t)+" alice@laptop")
	got := reconcileSSHKey(t, c, key)
	cond := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonValid || cond.ObservedGeneration != got.Generation || got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("status %+v", got.Status)
	}
}

func TestSSHKeyNotReadyWhenInvalid(t *testing.T) {
	c, _ := startEnv(t)
	key := newSSHKey(t, c, "ssh-rsa AAAAsecretmaterial")
	got := reconcileSSHKey(t, c, key)
	cond := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonInvalidPublicKey {
		t.Fatalf("status %+v", got.Status)
	}
	if strings.Contains(cond.Message, "AAAAsecretmaterial") {
		t.Fatalf("message echoes the key: %q", cond.Message)
	}
}

func TestSSHKeyRecovers(t *testing.T) {
	c, _ := startEnv(t)
	key := newSSHKey(t, c, "ssh-rsa AAAAbad")
	first := reconcileSSHKey(t, c, key)
	first.Spec.PublicKey = ed25519Key(t)
	if err := c.Update(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	got := reconcileSSHKey(t, c, key)
	cond := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || got.Status.ObservedGeneration != 2 {
		t.Fatalf("status %+v", got.Status)
	}
}
