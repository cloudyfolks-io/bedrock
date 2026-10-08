package images

import (
	"fmt"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const cirrosRef = "quay.io/kubevirt/cirros-container-disk-demo:v1.9.0@sha256:ebdb8d8b9b480f6ee7664ed3fdde8428767664f507d98f94090edeff04d7ebf2"

func fromYAML(t *testing.T, text string) *unstructured.Unstructured {
	t.Helper()
	body, err := sigyaml.YAMLToJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(body); err != nil {
		t.Fatal(err)
	}
	return obj
}

func ubuntuSubject(spec v1alpha1.ImageSpec) Subject {
	img := ubuntuImage()
	img.Spec = spec
	return SubjectOfImage(img, "acme", "web")
}

func registrySpec(image string) v1alpha1.ImageSpec {
	return v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Registry: &v1alpha1.ImageRegistrySource{Image: image}}, SizeGi: 10}
}

func nested(t *testing.T, obj *unstructured.Unstructured, fields ...string) any {
	t.Helper()
	value, _, err := unstructured.NestedFieldNoCopy(obj.Object, fields...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDataImportCronForARegistryImage(t *testing.T) {
	spec := registrySpec("quay.io/containerdisks/ubuntu:24.04")
	spec.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *", Keep: 2}
	want := fromYAML(t, `
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataImportCron
metadata:
  name: ubuntu
  namespace: acme-web
  labels:
    bedrock.cloudyfolks.io/kind: Image
    bedrock.cloudyfolks.io/name: ubuntu
    bedrock.cloudyfolks.io/tenant: acme
    bedrock.cloudyfolks.io/project: web
  ownerReferences:
    - apiVersion: bedrock.cloudyfolks.io/v1alpha1
      kind: Image
      name: ubuntu
      uid: 11111111-2222-3333-4444-555555555555
      controller: true
      blockOwnerDeletion: true
spec:
  schedule: 0 3 * * *
  managedDataSource: ubuntu
  garbageCollect: Outdated
  importsToKeep: 2
  retentionPolicy: None
  template:
    spec:
      source:
        registry:
          url: docker://quay.io/containerdisks/ubuntu:24.04
          pullMethod: node
      storage:
        storageClassName: block
        resources:
          requests:
            storage: 10Gi
`)
	got := DataImportCron(ubuntuSubject(spec))
	if !reflect.DeepEqual(got.Object, want.Object) {
		t.Fatalf("cron\n got %v\nwant %v", got.Object, want.Object)
	}
}

func TestRegistryWithoutPullSecretKeepsTagAndDigest(t *testing.T) {
	dv := ImportDataVolume(ubuntuSubject(registrySpec(cirrosRef)), 1)
	if got := nested(t, dv, "spec", "source", "registry", "url"); got != "docker://"+cirrosRef {
		t.Fatalf("url %v", got)
	}
	if got := nested(t, dv, "spec", "source", "registry", "pullMethod"); got != "node" {
		t.Fatalf("pull method %v", got)
	}
}

func TestRegistryWithPullSecretUsesPodPull(t *testing.T) {
	spec := registrySpec(cirrosRef)
	spec.Source.Registry.PullSecretRef = "registry-auth"
	dv := ImportDataVolume(ubuntuSubject(spec), 1)
	registry := nested(t, dv, "spec", "source", "registry").(map[string]any)
	want := map[string]any{
		"url":        "docker://quay.io/kubevirt/cirros-container-disk-demo@sha256:ebdb8d8b9b480f6ee7664ed3fdde8428767664f507d98f94090edeff04d7ebf2",
		"pullMethod": "pod",
		"secretRef":  "registry-auth",
	}
	if !reflect.DeepEqual(registry, want) {
		t.Fatalf("registry %v", registry)
	}

	tagOnly := registrySpec("registry.example.com:5000/team/disk:1.2")
	tagOnly.Source.Registry.PullSecretRef = "registry-auth"
	dv = ImportDataVolume(ubuntuSubject(tagOnly), 1)
	if got := nested(t, dv, "spec", "source", "registry", "url"); got != "docker://registry.example.com:5000/team/disk:1.2" {
		t.Fatalf("url %v", got)
	}
}

func TestImportDataVolumeSources(t *testing.T) {
	cases := map[string]struct {
		spec   v1alpha1.ImageSpec
		size   int32
		source map[string]any
	}{
		"registry": {
			spec:   registrySpec("quay.io/containerdisks/ubuntu:24.04"),
			size:   10,
			source: map[string]any{"registry": map[string]any{"url": "docker://quay.io/containerdisks/ubuntu:24.04", "pullMethod": "node"}},
		},
		"http": {
			spec: v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: "https://example.com/d.qcow2", Checksum: "sha256:abc", SecretRef: "http-auth"}}},
			size: 20,
			source: map[string]any{"http": map[string]any{
				"url": "https://example.com/d.qcow2", "checksum": "sha256:abc", "secretRef": "http-auth",
			}},
		},
		"upload": {
			spec:   v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}},
			size:   5,
			source: map[string]any{"upload": map[string]any{}},
		},
		"volumeRef": {
			spec:   v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{VolumeRef: &v1alpha1.ImageVolumeSource{Name: "web-root"}}},
			size:   12,
			source: map[string]any{"pvc": map[string]any{"namespace": "acme-web", "name": "web-root"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := ubuntuSubject(tc.spec)
			dv := ImportDataVolume(s, tc.size)
			if dv.GetName() != "ubuntu-import" || dv.GetKind() != "DataVolume" {
				t.Fatalf("object %s %s", dv.GetKind(), dv.GetName())
			}
			annotations := dv.GetAnnotations()
			if annotations["cdi.kubevirt.io/storage.bind.immediate.requested"] != "true" || annotations[AnnotationSourceHash] != SourceHash(tc.spec) {
				t.Fatalf("annotations %v", annotations)
			}
			if got := nested(t, dv, "spec", "source"); !reflect.DeepEqual(got, tc.source) {
				t.Fatalf("source %v", got)
			}
			storage := nested(t, dv, "spec", "storage").(map[string]any)
			if storage["storageClassName"] != "block" {
				t.Fatalf("storage %v", storage)
			}
			if got := nested(t, dv, "spec", "storage", "resources", "requests", "storage"); got != fmt.Sprintf("%dGi", tc.size) {
				t.Fatalf("size %v", got)
			}
		})
	}
}

func TestHTTPSourceCertConfigMap(t *testing.T) {
	spec := v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: "https://example.com/d.qcow2"}}}
	img := ubuntuImage()
	img.Spec = spec
	cert := func(s Subject, extraCA bool) (any, bool) {
		s.ExtraCA = extraCA
		value, found, _ := unstructured.NestedFieldNoCopy(ImportDataVolume(s, 10).Object, "spec", "source", "http", "certConfigMap")
		return value, found
	}

	if _, found := cert(SubjectOfImage(img, "acme", "web"), false); found {
		t.Fatal("no certConfigMap expected")
	}
	if got, _ := cert(SubjectOfImage(img, "acme", "web"), true); got != "platform-additional-ca" {
		t.Fatalf("extra CA %v", got)
	}
	img.Spec.Source.HTTP.CAConfigMap = "corp-ca"
	if got, _ := cert(SubjectOfImage(img, "acme", "web"), true); got != "corp-ca" {
		t.Fatalf("own CA %v", got)
	}
}

func TestISOIsMarkedReadOnly(t *testing.T) {
	iso := ubuntuSubject(v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeISO, Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}})
	disk := ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04"))
	build := func(s Subject) []*unstructured.Unstructured {
		return []*unstructured.Unstructured{ImportDataVolume(s, 2), GoldenSnapshot(s, "ubuntu-import", "sha256:abc"), DataSource(s, "ubuntu", "snap")}
	}
	for _, obj := range build(iso) {
		if obj.GetAnnotations()[AnnotationAttach] != "read-only" {
			t.Fatalf("%s must carry the attach marker: %v", obj.GetKind(), obj.GetAnnotations())
		}
	}
	for _, obj := range build(disk) {
		if _, found := obj.GetAnnotations()[AnnotationAttach]; found {
			t.Fatalf("%s must not carry the attach marker", obj.GetKind())
		}
	}
}

func TestGoldenSnapshot(t *testing.T) {
	s := ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04"))
	snap := GoldenSnapshot(s, "ubuntu-import", "sha256:abc")
	if snap.GetName() != GoldenName("ubuntu", "sha256:abc") || snap.GetAPIVersion() != "snapshot.storage.k8s.io/v1" {
		t.Fatalf("snapshot %s %s", snap.GetAPIVersion(), snap.GetName())
	}
	if got := nested(t, snap, "spec", "volumeSnapshotClassName"); got != "block" {
		t.Fatalf("class %v", got)
	}
	if got := nested(t, snap, "spec", "source", "persistentVolumeClaimName"); got != "ubuntu-import" {
		t.Fatalf("claim %v", got)
	}
	annotations := snap.GetAnnotations()
	if annotations[AnnotationDigest] != "sha256:abc" || annotations[AnnotationSourceHash] != SourceHash(s.Spec) {
		t.Fatalf("annotations %v", annotations)
	}
}

func TestDataSourceForASnapshot(t *testing.T) {
	s := ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04"))
	golden := GoldenName("ubuntu", "sha256:abc")
	for _, name := range []string{"ubuntu", golden} {
		ds := DataSource(s, name, golden)
		want := map[string]any{"snapshot": map[string]any{"name": golden, "namespace": "acme-web"}}
		if ds.GetName() != name || !reflect.DeepEqual(nested(t, ds, "spec", "source"), want) {
			t.Fatalf("data source %s %v", ds.GetName(), ds.Object)
		}
	}
}

func TestClusterImageObjectsLiveInTheCatalogNamespace(t *testing.T) {
	catalog := ubuntuImage()
	cluster := v1alpha1.ClusterImage{ObjectMeta: catalog.ObjectMeta, Spec: registrySpec("quay.io/containerdisks/ubuntu:24.04")}
	cluster.Namespace = ""
	s := SubjectOfClusterImage(cluster)
	dv := ImportDataVolume(s, 10)
	if dv.GetNamespace() != "bedrock-images" || len(dv.GetLabels()) != 2 {
		t.Fatalf("object %s %v", dv.GetNamespace(), dv.GetLabels())
	}
	if owners := dv.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "ClusterImage" {
		t.Fatalf("owners %v", owners)
	}
}
