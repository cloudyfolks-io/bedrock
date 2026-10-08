package images

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func httpSpec(url string) v1alpha1.ImageSpec {
	return v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: url}}}
}

func TestImportName(t *testing.T) {
	if got := ImportName("ubuntu"); got != "ubuntu-import" {
		t.Fatalf("name %q", got)
	}
}

func TestGoldenNameUsesTheDigest(t *testing.T) {
	a := GoldenName("ubuntu-24.04", "sha256:aaa")
	if a != GoldenName("ubuntu-24.04", "sha256:aaa") {
		t.Fatal("same digest must give the same name")
	}
	if a == GoldenName("ubuntu-24.04", "sha256:bbb") {
		t.Fatal("different digests must give different names")
	}
	if !strings.HasPrefix(a, "ubuntu-24.04-") || len(a) != len("ubuntu-24.04-")+12 {
		t.Fatalf("name %q", a)
	}
	if errs := validation.IsDNS1123Subdomain(a); len(errs) != 0 {
		t.Fatalf("name %q is invalid: %v", a, errs)
	}
}

func TestSourceHashChangesWithTheSource(t *testing.T) {
	base := httpSpec("https://example.com/a.qcow2")
	hash := SourceHash(base)
	if len(hash) != 16 {
		t.Fatalf("hash %q", hash)
	}

	otherURL := httpSpec("https://example.com/b.qcow2")
	bigger := base
	bigger.SizeGi = 20
	iso := base
	iso.Type = v1alpha1.ImageTypeISO
	for name, spec := range map[string]v1alpha1.ImageSpec{"url": otherURL, "size": bigger, "type": iso} {
		if SourceHash(spec) == hash {
			t.Fatalf("%s must change the hash", name)
		}
	}

	same := base
	same.OS = &v1alpha1.ImageOS{Family: "rocky"}
	same.Type = v1alpha1.ImageTypeDisk
	if SourceHash(same) != hash {
		t.Fatal("os and an explicit disk type must not change the hash")
	}
	registry := v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Registry: &v1alpha1.ImageRegistrySource{Image: "r/i:1"}}}
	withRefresh := registry
	withRefresh.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *"}
	if SourceHash(registry) != SourceHash(withRefresh) {
		t.Fatal("refresh must not change the hash")
	}
}

func TestSizeGiAndKeepDefaults(t *testing.T) {
	var spec v1alpha1.ImageSpec
	if SizeGi(spec) != 10 || Keep(spec) != 3 {
		t.Fatalf("defaults %d %d", SizeGi(spec), Keep(spec))
	}
	spec.SizeGi = 20
	spec.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *", Keep: 2}
	if SizeGi(spec) != 20 || Keep(spec) != 2 {
		t.Fatalf("explicit %d %d", SizeGi(spec), Keep(spec))
	}
	spec.Refresh.Keep = 0
	if Keep(spec) != 3 {
		t.Fatalf("keep %d", Keep(spec))
	}
}

func TestUsesCron(t *testing.T) {
	registry := v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Registry: &v1alpha1.ImageRegistrySource{Image: "r/i:1"}}}
	if UsesCron(registry) {
		t.Fatal("a registry without refresh uses a DataVolume")
	}
	registry.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *"}
	if !UsesCron(registry) {
		t.Fatal("a registry with a schedule uses a cron")
	}
	if UsesCron(httpSpec("https://example.com/a")) {
		t.Fatal("http never uses a cron")
	}
}

func TestReadOnlyAttach(t *testing.T) {
	if ReadOnlyAttach(v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeDisk}) || !ReadOnlyAttach(v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeISO}) {
		t.Fatal("only an ISO is attached read-only")
	}
}

func TestDataSourceKeys(t *testing.T) {
	img := v1alpha1.Image{ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "acme-web"}}
	if _, ok := DataSourceKeyOfImage(img); ok {
		t.Fatal("an image that is not Ready has no key")
	}
	img.Status.Phase = v1alpha1.ImagePhaseReady
	if _, ok := DataSourceKeyOfImage(img); ok {
		t.Fatal("an image without a data source has no key")
	}
	img.Status.DataSource = "ubuntu"
	key, ok := DataSourceKeyOfImage(img)
	if !ok || key.Namespace != "acme-web" || key.Name != "ubuntu" {
		t.Fatalf("key %v %v", key, ok)
	}

	catalog := v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-24.04"}}
	catalog.Status.Phase = v1alpha1.ImagePhaseReady
	catalog.Status.DataSource = "ubuntu-24.04"
	key, ok = DataSourceKeyOfClusterImage(catalog)
	if !ok || key.Namespace != "bedrock-images" || key.Name != "ubuntu-24.04" {
		t.Fatalf("key %v %v", key, ok)
	}
}
