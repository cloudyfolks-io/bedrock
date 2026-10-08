package images

import (
	"context"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func applyAll(t *testing.T, c client.Client, objects ...*unstructured.Unstructured) {
	t.Helper()
	for _, obj := range objects {
		if err := ssa.Apply(context.Background(), c, obj, "test"); err != nil {
			t.Fatalf("apply %s/%s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

func objectNames(items []unstructured.Unstructured) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.GetName())
	}
	slices.Sort(names)
	return names
}

func TestObserveFindsTheWorld(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04"))
	s.Spec.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *"}

	otherImage := ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04"))
	otherImage.Name = "other"
	otherImage.Labels = map[string]string{v1alpha1.LabelKind: "Image", v1alpha1.LabelName: "other"}

	cdiSnapshot := GoldenSnapshot(otherImage, "ubuntu-import", "sha256:cdi")
	cdiSnapshot.SetName("ubuntu-cdi000000000")
	cdiSnapshot.SetLabels(map[string]string{labelDataImportCron: "ubuntu"})
	cdiSnapshot.SetOwnerReferences(nil)

	applyAll(t, c,
		DataImportCron(s),
		ImportDataVolume(s, 10),
		GoldenSnapshot(s, "ubuntu-import", "sha256:bedrock"),
		cdiSnapshot,
		GoldenSnapshot(otherImage, "ubuntu-import", "sha256:other"),
		DataSource(s, "ubuntu-version", "snap"),
		DataSource(s, "ubuntu", "snap"),
		DataSource(otherImage, "unrelated", "snap"),
	)
	createClaim(t, c, "acme-web", "ubuntu-import", "10Gi")

	world, err := Observe(context.Background(), c, s, v1alpha1.ImageStatus{Phase: "Pending"}, "cloud.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if world.Cron == nil || world.DataVolume == nil || world.ImportClaim == nil || world.PlatformHost != "cloud.example.com" || world.Prev.Phase != "Pending" {
		t.Fatalf("world %+v", world)
	}
	if got := objectNames(world.Snapshots); !slices.Equal(got, []string{"ubuntu-cdi000000000", goldenNameOf(s, "sha256:bedrock")}) {
		t.Fatalf("snapshots %v", got)
	}
	if got := objectNames(world.DataSources); !slices.Equal(got, []string{"ubuntu", "ubuntu-version"}) {
		t.Fatalf("data sources %v", got)
	}
}

func TestObserveOfAnEmptyWorld(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := ubuntuSubject(v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{VolumeRef: &v1alpha1.ImageVolumeSource{Name: "web-root"}}})
	world, err := Observe(context.Background(), c, s, v1alpha1.ImageStatus{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if world.Cron != nil || world.DataVolume != nil || world.ImportClaim != nil || world.SourceClaim != nil || len(world.Snapshots) != 0 || len(world.DataSources) != 0 {
		t.Fatalf("world %+v", world)
	}
}

func TestObserveUsesTheCronImportDataVolume(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04"))
	s.Spec.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *"}
	applyAll(t, c, DataImportCron(s))
	patchObjectStatus(t, c, DataImportCronGVK, "acme-web", "ubuntu", map[string]any{
		"currentImports": []any{map[string]any{"DataVolumeName": "ubuntu-1a2b3c4d5e6f", "Digest": "sha256:1a2b"}},
	})
	imported := ImportDataVolume(s, 10)
	imported.SetName("ubuntu-1a2b3c4d5e6f")
	applyAll(t, c, imported)

	world, err := Observe(context.Background(), c, s, v1alpha1.ImageStatus{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if world.DataVolume == nil || world.DataVolume.GetName() != "ubuntu-1a2b3c4d5e6f" {
		t.Fatalf("data volume %v", world.DataVolume)
	}
}
