package registry

import (
	"context"
	"net/http"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

func rookUser(name string, phase string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephObjectStoreUser",
		"metadata":   map[string]any{"name": name, "namespace": "rook-ceph"},
		"spec":       map[string]any{"store": "bedrock-rgw", "displayName": name},
	}}
	if phase != "" {
		obj.Object["status"] = map[string]any{"phase": phase}
	}
	return obj
}

func rookSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-object-user-bedrock-rgw-" + name},
		Data:       map[string][]byte{"AccessKey": []byte("access-" + name), "SecretKey": []byte("secret-" + name)},
	}
}

func platformSettings(extra ...client.Object) []client.Object {
	base := []client.Object{
		addonObject("registry", true), addonObject("cnpg", true),
		setting("registry.host", "registry.example.com"), setting("database.enabled", "true"),
	}
	return append(base, extra...)
}

func readyStores() []client.Object {
	return []client.Object{rookSecret("bedrock-registry"), rookSecret("database-backups-s3")}
}

func newReconciler(t *testing.T, endpoint string, objects ...client.Object) (*AddonReconciler, client.Client) {
	t.Helper()
	c := fakeCluster(t, objects...)
	for _, name := range []string{"bedrock-registry", "database-backups-s3"} {
		if err := c.Create(context.Background(), rookUser(name, "Ready")); err != nil {
			t.Fatal(err)
		}
	}
	return &AddonReconciler{Client: c, S3: &s3.Client{HTTP: http.DefaultClient}, Endpoint: endpoint, Random: seeded(4)}, c
}

func reconcileOnce(t *testing.T, r *AddonReconciler) ctrl.Result {
	t.Helper()
	got, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func runtimeCondition(t *testing.T, c client.Client) *metav1.Condition {
	t.Helper()
	var addon v1alpha1.Addon
	if err := c.Get(context.Background(), client.ObjectKey{Name: "registry"}, &addon); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(addon.Status.Conditions, ConditionRuntime)
}

func exists(c client.Client, apiVersion, kind, name string) bool {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: name}, obj)
	return err == nil
}

func TestADisabledAddonMakesNothing(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, addonObject("registry", false), addonObject("cnpg", true), setting("registry.host", "registry.example.com"), setting("database.enabled", "true"))
	reconcileOnce(t, r)
	if exists(c, "v1", "Secret", "harbor-admin") || runtimeCondition(t, c) != nil {
		t.Fatal("a disabled addon made objects")
	}
}

func TestMissingAddonObjectMakesNothing(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, setting("registry.host", "registry.example.com"))
	reconcileOnce(t, r)
	if exists(c, "v1", "Secret", "harbor-admin") {
		t.Fatal("objects without an addon")
	}
}

func TestPrerequisitesGateTheRuntime(t *testing.T) {
	cases := []struct {
		name    string
		objects []client.Object
		reason  string
	}{
		{"no host", []client.Object{addonObject("registry", true), addonObject("cnpg", true), setting("database.enabled", "true")}, "HostNotSet"},
		{"cnpg off", []client.Object{addonObject("registry", true), setting("registry.host", "r.example.com"), setting("database.enabled", "true")}, "CNPGAddonOff"},
		{"database off", []client.Object{addonObject("registry", true), addonObject("cnpg", true), setting("registry.host", "r.example.com")}, "DatabaseDisabled"},
	}
	for _, c := range cases {
		server := fakeS3(t, http.StatusOK, "")
		r, cluster := newReconciler(t, server.URL, c.objects...)
		reconcileOnce(t, r)
		condition := runtimeCondition(t, cluster)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != c.reason {
			t.Errorf("%s: condition %+v", c.name, condition)
		}
		if exists(cluster, "v1", "Secret", "harbor-admin") {
			t.Errorf("%s: a Secret exists", c.name)
		}
	}
}

func TestRuntimeObjectsAreMadeWhenEverythingIsReady(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, append(platformSettings(), readyStores()...)...)
	reconcileOnce(t, r)
	condition := runtimeCondition(t, c)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "Ready" {
		t.Fatalf("condition %+v", condition)
	}
	for _, name := range []string{"harbor-admin", "harbor-core-keys", "harbor-jobservice-keys", "harbor-registry-keys", "harbor-registry-credentials", "harbor-db-credentials", "bedrock-registry", "database-backups-s3"} {
		getSecret(t, c, name)
	}
	var namespace corev1.Namespace
	if err := c.Get(context.Background(), client.ObjectKey{Name: "bedrock-registry"}, &namespace); err != nil {
		t.Fatal(err)
	}
	for _, check := range [][3]string{
		{"cert-manager.io/v1", "Certificate", "harbor-token"},
		{"cert-manager.io/v1", "Certificate", "registry-public"},
		{"postgresql.cnpg.io/v1", "Cluster", "harbor-db"},
		{"barmancloud.cnpg.io/v1", "ObjectStore", "harbor-db-backups"},
		{"postgresql.cnpg.io/v1", "ScheduledBackup", "harbor-db-daily"},
		{"traefik.io/v1alpha1", "IngressRoute", "harbor-public"},
	} {
		if !exists(c, check[0], check[1], check[2]) {
			t.Errorf("missing %s %s", check[1], check[2])
		}
	}
	if got := string(getSecret(t, c, "bedrock-registry").Data["REGISTRY_STORAGE_S3_ACCESSKEY"]); got != "access-bedrock-registry" {
		t.Errorf("image store key %q", got)
	}
	if got := string(getSecret(t, c, "database-backups-s3").Data["AWS_SECRET_ACCESS_KEY"]); got != "secret-database-backups-s3" {
		t.Errorf("backup store key %q", got)
	}
}

func TestASecondRunChangesNothing(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, append(platformSettings(), readyStores()...)...)
	reconcileOnce(t, r)
	first := string(getSecret(t, c, "harbor-admin").Data["HARBOR_ADMIN_PASSWORD"])
	r.Random = seeded(99)
	reconcileOnce(t, r)
	if got := string(getSecret(t, c, "harbor-admin").Data["HARBOR_ADMIN_PASSWORD"]); got != first {
		t.Errorf("the admin password changed from %q to %q", first, got)
	}
}

func TestAStorePendingUntilTheRookUserIsReady(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	c := fakeCluster(t, append(platformSettings(), readyStores()...)...)
	if err := c.Create(context.Background(), rookUser("bedrock-registry", "")); err != nil {
		t.Fatal(err)
	}
	r := &AddonReconciler{Client: c, S3: &s3.Client{HTTP: http.DefaultClient}, Endpoint: server.URL, Random: seeded(4)}
	reconcileOnce(t, r)
	condition := runtimeCondition(t, c)
	if condition == nil || condition.Reason != "StorePending" || condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", condition)
	}
	if exists(c, "postgresql.cnpg.io/v1", "Cluster", "harbor-db") {
		t.Error("the database was made before the store was ready")
	}
}

func TestABucketOfAnotherOwnerIsReported(t *testing.T) {
	server := fakeS3(t, http.StatusConflict, "BucketAlreadyExists")
	r, c := newReconciler(t, server.URL, append(platformSettings(), readyStores()...)...)
	reconcileOnce(t, r)
	condition := runtimeCondition(t, c)
	if condition == nil || condition.Reason != "BucketNotOwned" || condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", condition)
	}
	if exists(c, "postgresql.cnpg.io/v1", "Cluster", "harbor-db") {
		t.Error("the database was made while the bucket was foreign")
	}
}

func TestRemovingAnAliasRemovesItsRoute(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, append(platformSettings(setting("registry.aliases", "mirror.example.com")), readyStores()...)...)
	reconcileOnce(t, r)
	if !exists(c, "traefik.io/v1alpha1", "IngressRoute", "harbor-public-mirror-example-com") || !exists(c, "traefik.io/v1alpha1", "Middleware", "harbor-realm-mirror-example-com") {
		t.Fatal("the alias route was not made")
	}
	var aliases v1alpha1.Setting
	if err := c.Get(context.Background(), client.ObjectKey{Name: "registry.aliases"}, &aliases); err != nil {
		t.Fatal(err)
	}
	aliases.Spec.Value = ""
	if err := c.Update(context.Background(), &aliases); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r)
	if exists(c, "traefik.io/v1alpha1", "IngressRoute", "harbor-public-mirror-example-com") || exists(c, "traefik.io/v1alpha1", "Middleware", "harbor-realm-mirror-example-com") {
		t.Error("the alias route stayed")
	}
	if !exists(c, "traefik.io/v1alpha1", "IngressRoute", "harbor-public") {
		t.Error("the primary route is gone")
	}
}

func TestInvalidSettingsAreReportedWithoutChangingObjects(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, append(platformSettings(setting("registry.db-instances", "9")), readyStores()...)...)
	reconcileOnce(t, r)
	condition := runtimeCondition(t, c)
	if condition == nil || condition.Reason != "InvalidSettings" {
		t.Fatalf("condition %+v", condition)
	}
	if exists(c, "v1", "Secret", "harbor-admin") {
		t.Error("objects were made from invalid settings")
	}
}

func TestTheDatabaseGetsTheConfiguredInstances(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, append(platformSettings(setting("registry.db-instances", "3"), setting("registry.storage-class", "fast")), readyStores()...)...)
	reconcileOnce(t, r)
	cluster := &unstructured.Unstructured{}
	cluster.SetAPIVersion("postgresql.cnpg.io/v1")
	cluster.SetKind("Cluster")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: "harbor-db"}, cluster); err != nil {
		t.Fatal(err)
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	class, _, _ := unstructured.NestedString(cluster.Object, "spec", "storage", "storageClass")
	if instances != 3 || class != "fast" {
		t.Errorf("instances %d class %q", instances, class)
	}
}

func node(name, address string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: address}}}}
}

func TestTheJobserviceEgressPolicyBlocksTheNodesAndTheConfiguredRanges(t *testing.T) {
	server := fakeS3(t, http.StatusOK, "")
	r, c := newReconciler(t, server.URL, append(platformSettings(setting("registry.webhook-blocked-cidrs", "172.20.0.0/16"), node("n1", "192.168.5.10")), readyStores()...)...)
	reconcileOnce(t, r)
	policy := &unstructured.Unstructured{}
	policy.SetAPIVersion("networking.k8s.io/v1")
	policy.SetKind("NetworkPolicy")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: "harbor-jobservice-egress"}, policy); err != nil {
		t.Fatal(err)
	}
	block := egressRules(t, policy)[2]["to"].([]any)[0].(map[string]any)["ipBlock"].(map[string]any)
	except := []string{}
	for _, entry := range block["except"].([]any) {
		except = append(except, entry.(string))
	}
	if !slices.Equal(except, []string{"172.20.0.0/16", "192.168.5.10/32"}) {
		t.Errorf("except %v", except)
	}
}
