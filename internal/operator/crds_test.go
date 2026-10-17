package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestCRDsRoundTrip(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.host"}, Spec: v1alpha1.SettingSpec{Value: "lab.example"}}
	if err := c.Create(ctx, setting); err != nil {
		t.Fatal(err)
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Spec: v1alpha1.HostSpec{Roles: []string{v1alpha1.RoleControlPlane}}}
	if err := c.Create(ctx, host); err != nil {
		t.Fatal(err)
	}
	bad := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}, Spec: v1alpha1.HostSpec{Roles: []string{"storage"}}}
	if err := c.Create(ctx, bad); err == nil {
		t.Fatal("invalid role must be rejected by the CRD schema")
	}
	release := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.1.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.1.0", Image: "ghcr.io/cloudyfolks-io/bedrock/release:v0.1.0", K0sVersion: "v1.36.3+k0s.0"}}
	if err := c.Create(ctx, release); err != nil {
		t.Fatal(err)
	}
	release.Spec.K0sVersion = "v1.36.4+k0s.0"
	if err := c.Update(ctx, release); err == nil {
		t.Fatal("release spec must be immutable")
	}

	var got v1alpha1.Setting
	if err := c.Get(ctx, client.ObjectKey{Name: "platform.host"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Value != "lab.example" {
		t.Fatalf("value %q", got.Spec.Value)
	}
}

func TestClusterJoinCIDRDefaultsAndIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	cluster := &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName},
		Spec:       v1alpha1.ClusterSpec{DesiredVersion: "v0.1.0", API: v1alpha1.APISpec{VIP: "10.0.0.10"}},
	}
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.Spec.JoinCIDR != "100.64.0.0/16" {
		t.Fatalf("joinCIDR must default to the kube-ovn join subnet: %q", cluster.Spec.JoinCIDR)
	}
	cluster.Spec.NodeConcurrency = 2
	if err := c.Update(ctx, cluster); err != nil {
		t.Fatalf("updating another field must not trip the joinCIDR immutability rule: %v", err)
	}
	cluster.Spec.JoinCIDR = "100.99.0.0/16"
	if err := c.Update(ctx, cluster); err == nil {
		t.Fatal("joinCIDR must be immutable")
	}
}

func clusterWithEncryption(encryption v1alpha1.EncryptionSpec) *v1alpha1.Cluster {
	return &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName},
		Spec:       v1alpha1.ClusterSpec{DesiredVersion: "v0.1.0", API: v1alpha1.APISpec{VIP: "10.0.0.10"}, Encryption: encryption},
	}
}

func currentCluster(t *testing.T, c client.Client) *v1alpha1.Cluster {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	return &cluster
}

func kmsEncryption(endpoint string) v1alpha1.EncryptionSpec {
	return v1alpha1.EncryptionSpec{KMS: &v1alpha1.KMSSpec{Endpoint: endpoint}}
}

func TestClusterKMSIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithEncryption(kmsEncryption("unix:///run/kms/kms.sock")))

	changed := currentCluster(t, c)
	changed.Spec.Encryption.KMS.Endpoint = "unix:///run/kms/other.sock"
	updateInvalid(t, c, changed, "encryption.kms is set at install and is immutable")

	removed := currentCluster(t, c)
	removed.Spec.Encryption = v1alpha1.EncryptionSpec{}
	updateInvalid(t, c, removed, "encryption.kms is set at install and is immutable")

	window := currentCluster(t, c)
	window.Spec.MaintenanceWindow = "Sun 02:00-04:00"
	if err := c.Update(ctx, window); err != nil {
		t.Fatalf("updating another field must not trip the KMS immutability rule: %v", err)
	}
}

func TestClusterKMSCannotBeAdded(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithEncryption(v1alpha1.EncryptionSpec{}))

	added := currentCluster(t, c)
	added.Spec.Encryption = kmsEncryption("unix:///run/kms/kms.sock")
	updateInvalid(t, c, added, "encryption.kms is set at install and is immutable")

	concurrency := currentCluster(t, c)
	concurrency.Spec.NodeConcurrency = 2
	if err := c.Update(ctx, concurrency); err != nil {
		t.Fatalf("updating another field must not trip the KMS immutability rule: %v", err)
	}
}

func TestClusterKMSEndpointPattern(t *testing.T) {
	c, _ := StartTestEnv(t)
	createInvalid(t, c, clusterWithEncryption(kmsEncryption("/run/kms.sock")), "should match")
}

const adoptImmutableMessage = "storage.adopt is set at install and is immutable"

func clusterWithAdopt(adopt *v1alpha1.AdoptSpec) *v1alpha1.Cluster {
	cluster := clusterWithEncryption(v1alpha1.EncryptionSpec{})
	cluster.Spec.Storage.Adopt = adopt
	return cluster
}

func TestClusterAdoptIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithAdopt(adoptSpec()))

	changes := map[string]func(*v1alpha1.AdoptSpec){
		"fsid":   func(adopt *v1alpha1.AdoptSpec) { adopt.FSID = "0f1e2d3c-4b5a-4968-8776-655443322110" },
		"device": func(adopt *v1alpha1.AdoptSpec) { adopt.Hosts[0].Devices = []string{"/dev/disk/by-id/wwn-0x9"} },
		"pool":   func(adopt *v1alpha1.AdoptSpec) { adopt.Pools.Block = "other-block" },
		"cephx": func(adopt *v1alpha1.AdoptSpec) {
			adopt.Cephx = &runtime.RawExtension{Raw: []byte(`{"daemon":{"keyType":"aes"}}`)}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := currentCluster(t, c)
			change(changed.Spec.Storage.Adopt)
			updateInvalid(t, c, changed, adoptImmutableMessage)
		})
	}

	removed := currentCluster(t, c)
	removed.Spec.Storage = v1alpha1.ClusterStorageSpec{}
	updateInvalid(t, c, removed, adoptImmutableMessage)

	kept := currentCluster(t, c)
	kept.Spec.NodeConcurrency = 2
	if err := c.Update(ctx, kept); err != nil {
		t.Fatalf("an update that keeps storage.adopt must pass: %v", err)
	}
}

func TestClusterAdoptCannotBeAdded(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createValid(t, c, clusterWithAdopt(nil))

	added := currentCluster(t, c)
	added.Spec.Storage.Adopt = adoptSpec()
	updateInvalid(t, c, added, adoptImmutableMessage)

	concurrency := currentCluster(t, c)
	concurrency.Spec.NodeConcurrency = 2
	if err := c.Update(ctx, concurrency); err != nil {
		t.Fatalf("updating another field must not trip the adopt immutability rule: %v", err)
	}
}
