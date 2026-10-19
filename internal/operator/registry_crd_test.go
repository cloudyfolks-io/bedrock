package operator

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func registryObject(name, projectName string) *v1alpha1.Registry {
	return &v1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.RegistryProjectSpec{Name: projectName},
	}
}

func TestRegistryValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	full := registryObject("full", "acme-images")
	full.Spec.QuotaGB = ptr.To(int64(10))
	full.Spec.ForceDestroy = true
	full.Spec.Retention = &v1alpha1.RegistryRetention{KeepLastN: ptr.To(int32(10)), RetainDays: ptr.To(int32(30)), TagPattern: "v*", RetainUntagged: true, Schedule: "daily"}
	full.Spec.Webhook = &v1alpha1.RegistryWebhook{URL: "https://hooks.example.test/x", AuthHeaderSecretRef: &v1alpha1.RegistrySecretKeyRef{Name: "hook", Key: "token"}}
	createValid(t, c, full)
	full.Status.Host = "registry.example.test"
	full.Status.ProjectName = "acme-images"
	full.Status.ProjectID = 7
	full.Status.UsedBytes = 10
	full.Status.HardBytes = 100
	full.Status.RepositoryCount = 2
	if err := c.Status().Update(ctx, full); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createValid(t, c, registryObject("unnamed", ""))
	unlimited := registryObject("unlimited", "")
	unlimited.Spec.QuotaGB = ptr.To(int64(0))
	createValid(t, c, unlimited)

	long := strings.Repeat("a", 31)
	for _, bad := range []string{"ab", "Upper-case", "-leading", "trailing-", "under_score", long} {
		createInvalid(t, c, registryObject("bad-"+bad[:2]+string(rune('a'+len(bad)%26)), bad), "spec.name")
	}

	negative := registryObject("negative", "")
	negative.Spec.QuotaGB = ptr.To(int64(-1))
	createInvalid(t, c, negative, "spec.quotaGB")
}

func TestRegistryRetentionRanges(t *testing.T) {
	c, _ := StartTestEnv(t)
	cases := []struct {
		name      string
		retention v1alpha1.RegistryRetention
		field     string
	}{
		{"keep-zero", v1alpha1.RegistryRetention{KeepLastN: ptr.To(int32(0))}, "keepLastN"},
		{"keep-big", v1alpha1.RegistryRetention{KeepLastN: ptr.To(int32(1001))}, "keepLastN"},
		{"days-zero", v1alpha1.RegistryRetention{RetainDays: ptr.To(int32(0))}, "retainDays"},
		{"days-big", v1alpha1.RegistryRetention{RetainDays: ptr.To(int32(3651))}, "retainDays"},
		{"schedule", v1alpha1.RegistryRetention{Schedule: "weekly"}, "schedule"},
	}
	for _, tc := range cases {
		registry := registryObject(tc.name, "")
		registry.Spec.Retention = &tc.retention
		createInvalid(t, c, registry, tc.field)
	}
	for _, edge := range []v1alpha1.RegistryRetention{{KeepLastN: ptr.To(int32(1))}, {KeepLastN: ptr.To(int32(1000))}, {RetainDays: ptr.To(int32(1))}, {RetainDays: ptr.To(int32(3650))}, {Schedule: "off"}, {Schedule: "hourly"}} {
		registry := registryObject("edge-"+strings.ToLower(edge.Schedule)+string(rune('a'+ptr.Deref(edge.KeepLastN, 0)%26))+string(rune('a'+ptr.Deref(edge.RetainDays, 0)%26)), "")
		registry.Spec.Retention = &edge
		createValid(t, c, registry)
	}
}

func TestRegistryWebhookNeedsAnHTTPURL(t *testing.T) {
	c, _ := StartTestEnv(t)
	for name, url := range map[string]string{"ftp": "ftp://example.test/x", "empty": "", "space": "https://a b"} {
		registry := registryObject("hook-"+name, "")
		registry.Spec.Webhook = &v1alpha1.RegistryWebhook{URL: url}
		createInvalid(t, c, registry, "url")
	}
	registry := registryObject("hook-ok", "")
	registry.Spec.Webhook = &v1alpha1.RegistryWebhook{URL: "http://hooks.example.test/x"}
	createValid(t, c, registry)
}

func TestRegistryNameIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	named := registryObject("named", "acme-named")
	createValid(t, c, named)
	named.Spec.Name = "acme-other"
	updateInvalid(t, c, named, "name is immutable")

	unnamed := registryObject("unnamed", "")
	createValid(t, c, unnamed)
	unnamed.Spec.Name = "acme-late"
	updateInvalid(t, c, unnamed, "name is immutable")

	var stored v1alpha1.Registry
	if err := c.Get(ctx, client.ObjectKeyFromObject(named), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.QuotaGB = ptr.To(int64(3))
	if err := c.Update(ctx, &stored); err != nil {
		t.Fatalf("other fields stay mutable: %v", err)
	}
}

func credentialObject(name, registry, permission string) *v1alpha1.RegistryCredential {
	return &v1alpha1.RegistryCredential{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.RegistryCredentialSpec{RegistryRef: v1alpha1.RegistryReference{Name: registry}, Permission: permission},
	}
}

func TestRegistryCredentialValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	for _, permission := range []string{"pull", "pushPull", "manage"} {
		credential := credentialObject("ok-"+strings.ToLower(permission), "images", permission)
		createValid(t, c, credential)
	}
	credential := credentialObject("status", "images", "pull")
	createValid(t, c, credential)
	credential.Status.SecretName = "status-docker"
	credential.Status.Username = "robot$acme+status"
	credential.Status.RobotID = 3
	credential.Status.RobotName = "status"
	credential.Status.SecretReset = "one"
	if err := c.Status().Update(ctx, credential); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createInvalid(t, c, credentialObject("bad-permission", "images", "admin"), "spec.permission")
	createInvalid(t, c, credentialObject("no-permission", "images", ""), "spec.permission")
	createInvalid(t, c, credentialObject("no-registry", "", "pull"), "spec.registryRef")
}

func TestRegistryCredentialSpecIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	credential := credentialObject("fixed", "images", "pull")
	createValid(t, c, credential)
	credential.Spec.Permission = "manage"
	updateInvalid(t, c, credential, "spec is immutable")
	var stored v1alpha1.RegistryCredential
	if err := c.Get(ctx, client.ObjectKeyFromObject(credential), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.RegistryRef.Name = "other"
	updateInvalid(t, c, &stored, "spec is immutable")
}
