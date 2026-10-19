package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestClusterEncryptionActionAcceptsRotateAndRevertOnly(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithEncryption(v1alpha1.EncryptionSpec{}))

	for _, action := range []string{v1alpha1.EncryptionActionRotate, v1alpha1.EncryptionActionRevert, ""} {
		cluster := currentCluster(t, c)
		cluster.Spec.Encryption.Action = action
		if err := c.Update(ctx, cluster); err != nil {
			t.Fatalf("action %q must be accepted: %v", action, err)
		}
	}
	bad := currentCluster(t, c)
	bad.Spec.Encryption.Action = "destroy"
	updateInvalid(t, c, bad, "Unsupported value")
}

func TestClusterEncryptionActionKeepsTheKMSRule(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithEncryption(kmsEncryption("unix:///run/kms/kms.sock")))

	cluster := currentCluster(t, c)
	cluster.Spec.Encryption.Action = v1alpha1.EncryptionActionRotate
	if err := c.Update(ctx, cluster); err != nil {
		t.Fatalf("setting the action must not trip the KMS immutability rule: %v", err)
	}
}

func TestClusterAndHostStatusHoldTheEncryptionFields(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithEncryption(v1alpha1.EncryptionSpec{}))
	cluster := currentCluster(t, c)
	now := metav1.Now()
	cluster.Status.Encryption = &v1alpha1.EncryptionRotationStatus{Phase: v1alpha1.RotationPromotingKey, OldKey: "key1", NewKey: "key2", RewrittenSecrets: 3, LastRotatedAt: &now, Message: "m"}
	if err := c.Status().Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if got := currentCluster(t, c).Status.Encryption; got == nil || got.NewKey != "key2" || got.RewrittenSecrets != 3 {
		t.Fatalf("status %+v", got)
	}

	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-enc"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	host.Status.Encryption = &v1alpha1.EncryptionFileStatus{Hash: "abc", RestartPending: true, FailedHash: "def"}
	if err := c.Status().Update(ctx, host); err != nil {
		t.Fatal(err)
	}
}
