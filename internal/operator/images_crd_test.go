package operator

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func registryImageSpec() v1alpha1.ImageSpec {
	return v1alpha1.ImageSpec{
		OS:      &v1alpha1.ImageOS{Family: "ubuntu", Version: "24.04", Preference: "ubuntu"},
		Source:  v1alpha1.ImageSource{Registry: &v1alpha1.ImageRegistrySource{Image: "ghcr.io/cloudyfolks-io/images/ubuntu:24.04"}},
		Refresh: &v1alpha1.ImageRefresh{Schedule: "0 3 * * *", Keep: 2},
		SizeGi:  10,
	}
}

func TestImageKindsValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	catalog := &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-24.04"}, Spec: registryImageSpec()}
	createValid(t, c, catalog)
	if catalog.Spec.Type != v1alpha1.ImageTypeDisk {
		t.Fatalf("type must default to disk: %q", catalog.Spec.Type)
	}

	catalog.Status.Phase = v1alpha1.ImagePhaseReady
	catalog.Status.Versions = []v1alpha1.ImageVersion{{Digest: "sha256:abc", DataSource: "ubuntu-24.04"}}
	if err := c.Status().Update(ctx, catalog); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	catalog.Status.Phase = "Done"
	requireInvalid(t, c.Status().Update(ctx, catalog), "Unsupported value")

	var fresh v1alpha1.ClusterImage
	if err := c.Get(ctx, client.ObjectKeyFromObject(catalog), &fresh); err != nil {
		t.Fatal(err)
	}
	fresh.Spec.Type = v1alpha1.ImageTypeISO
	updateInvalid(t, c, &fresh, "type is immutable")

	project := &v1alpha1.Image{
		ObjectMeta: metav1.ObjectMeta{Name: "installer", Namespace: "default"},
		Spec:       v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeISO, Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}},
	}
	createValid(t, c, project)
	project.Spec.Type = v1alpha1.ImageTypeDisk
	updateInvalid(t, c, project, "type is immutable")

	badType := &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "bad-type", Namespace: "default"}, Spec: registryImageSpec()}
	badType.Spec.Type = "floppy"
	createInvalid(t, c, badType, "Unsupported value")

	none := &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "no-source", Namespace: "default"}}
	createInvalid(t, c, none, "exactly one source must be set")

	two := &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "two-sources", Namespace: "default"}, Spec: registryImageSpec()}
	two.Spec.Source.HTTP = &v1alpha1.ImageHTTPSource{URL: "https://example.com/disk.qcow2"}
	createInvalid(t, c, two, "exactly one source must be set")
}

func TestImageRefreshNeedsRegistry(t *testing.T) {
	c, _ := StartTestEnv(t)

	httpSpec := v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: "https://example.com/disk.qcow2"}}}
	withRefresh := httpSpec
	withRefresh.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *"}

	createInvalid(t, c, &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "http-refresh", Namespace: "default"}, Spec: withRefresh}, "refresh needs a registry source")
	createValid(t, c, &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "http-plain", Namespace: "default"}, Spec: httpSpec})
	createValid(t, c, &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "registry-refresh", Namespace: "default"}, Spec: registryImageSpec()})
}

func TestImageStatusProgress(t *testing.T) {
	c, _ := StartTestEnv(t)

	catalog := &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "progress"}, Spec: registryImageSpec()}
	createValid(t, c, catalog)
	catalog.Status.Progress = "42.5%"
	if err := c.Status().Update(context.Background(), catalog); err != nil {
		t.Fatalf("progress must be writable through the status subresource: %v", err)
	}
}

func TestImageRefreshRefusedForISO(t *testing.T) {
	c, _ := StartTestEnv(t)

	iso := registryImageSpec()
	iso.Type = v1alpha1.ImageTypeISO
	createInvalid(t, c, &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "iso-refresh", Namespace: "default"}, Spec: iso}, "refresh is not allowed for an iso image")
	createInvalid(t, c, &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "iso-refresh"}, Spec: iso}, "refresh is not allowed for an iso image")

	iso.Refresh = nil
	createValid(t, c, &v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "iso-plain", Namespace: "default"}, Spec: iso})
	createValid(t, c, &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "iso-plain"}, Spec: iso})
}

func TestImageCatalogMapping(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	spec := registryImageSpec()
	spec.OS.DisplayName = "Ubuntu Server 24.04 LTS"
	spec.OS.Architecture = "amd64"
	uuid := &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "0f8b2c1e-6d4a-4f35-9a7e-3b1c5d2e8f40"}, Spec: spec}
	createValid(t, c, uuid)

	var fresh v1alpha1.ClusterImage
	if err := c.Get(ctx, client.ObjectKeyFromObject(uuid), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.OS.DisplayName != "Ubuntu Server 24.04 LTS" || fresh.Spec.OS.Architecture != "amd64" {
		t.Fatalf("catalog mapping was not stored: %+v", fresh.Spec.OS)
	}

	upper := &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "0F8B2C1E-6D4A-4F35-9A7E-3B1C5D2E8F40"}, Spec: registryImageSpec()}
	createInvalid(t, c, upper, "lowercase RFC 1123 subdomain")

	badArch := registryImageSpec()
	badArch.OS.Architecture = "x86_64"
	createInvalid(t, c, &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "bad-arch"}, Spec: badArch}, "Unsupported value")

	longName := registryImageSpec()
	longName.OS.DisplayName = strings.Repeat("a", 129)
	createInvalid(t, c, &v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "long-name"}, Spec: longName}, "Too long")
}
