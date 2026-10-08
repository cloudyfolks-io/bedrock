package images

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func httpSubject() Subject {
	return ubuntuSubject(httpSpec("https://example.com/rocky.qcow2"))
}

func namedObject(kind, name string) unstructured.Unstructured {
	obj := unstructured.Unstructured{Object: map[string]any{}}
	obj.SetKind(kind)
	obj.SetNamespace("acme-web")
	obj.SetName(name)
	return obj
}

func dataVolume(s Subject, phase, progress string) *unstructured.Unstructured {
	dv := ImportDataVolume(s, 10)
	dv.Object["status"] = map[string]any{"phase": phase, "progress": progress}
	return dv
}

func goldenSnapshot(s Subject, digest string, ready bool, age time.Duration) unstructured.Unstructured {
	snap := GoldenSnapshot(s, ImportName(s.Name), digest)
	snap.SetCreationTimestamp(metav1.NewTime(testNow.Add(-age)))
	snap.Object["status"] = map[string]any{"readyToUse": ready, "restoreSize": "10Gi"}
	return *snap
}

func dataSourceNamed(name string, ready bool) unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	ds := namedObject("DataSource", name)
	ds.Object["status"] = map[string]any{"conditions": []any{condition("Ready", status, "")}}
	return ds
}

func claim(uid string, size string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{}
	pvc.UID = types.UID(uid)
	if size != "" {
		pvc.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}
	}
	return pvc
}

func namesAndKinds(objects []*unstructured.Unstructured) []string {
	out := make([]string, 0, len(objects))
	for _, o := range objects {
		out = append(out, o.GetKind()+"/"+o.GetName())
	}
	slices.Sort(out)
	return out
}

func TestDecideVolumeCreatesTheDataVolume(t *testing.T) {
	plan := Decide(httpSubject(), World{}, testNow)
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, []string{"DataVolume/ubuntu-import"}) || len(plan.Delete) != 0 {
		t.Fatalf("plan %v %v", got, plan.Delete)
	}
	if plan.Result.Phase != "Pending" || plan.Result.Reason != "ImportScheduled" || plan.Requeue != 5*time.Second {
		t.Fatalf("result %+v %v", plan.Result, plan.Requeue)
	}
}

func TestDecideVolumeWaitsWhileImporting(t *testing.T) {
	s := httpSubject()
	plan := Decide(s, World{DataVolume: dataVolume(s, "ImportInProgress", "42.5%")}, testNow)
	if plan.Result.Phase != "Importing" || plan.Result.Progress != "42.5%" || len(plan.Apply) != 0 || len(plan.Delete) != 0 {
		t.Fatalf("plan %+v", plan)
	}
}

func TestDecideVolumeSnapshotsASucceededImport(t *testing.T) {
	s := httpSubject()
	plan := Decide(s, World{DataVolume: dataVolume(s, "Succeeded", ""), ImportClaim: claim("abc", "")}, testNow)
	want := goldenNameOf(s, "uid:abc")
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, []string{"VolumeSnapshot/" + want}) {
		t.Fatalf("apply %v", got)
	}
	if plan.Apply[0].GetAnnotations()[AnnotationDigest] != "uid:abc" {
		t.Fatalf("annotations %v", plan.Apply[0].GetAnnotations())
	}
	if plan.Result.Phase != "Importing" || plan.Result.Reason != "Snapshotting" {
		t.Fatalf("result %+v", plan.Result)
	}

	missing := Decide(s, World{DataVolume: dataVolume(s, "Succeeded", "")}, testNow)
	if missing.Result.Reason != "ClaimNotFound" || len(missing.Apply) != 0 {
		t.Fatalf("missing claim %+v", missing)
	}
}

func TestDecideVolumeReadyWhenTheGoldenSnapshotIsReady(t *testing.T) {
	s := httpSubject()
	golden := goldenSnapshot(s, "uid:abc", true, time.Minute)
	world := World{
		DataVolume:  dataVolume(s, "Succeeded", ""),
		Snapshots:   []unstructured.Unstructured{golden},
		DataSources: []unstructured.Unstructured{dataSourceNamed("ubuntu", true)},
	}
	plan := Decide(s, world, testNow)
	wantApply := []string{"DataSource/" + golden.GetName(), "DataSource/ubuntu"}
	slices.Sort(wantApply)
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, wantApply) {
		t.Fatalf("apply %v", got)
	}
	if got := namesAndKinds(plan.Delete); !slices.Equal(got, []string{"DataVolume/ubuntu-import"}) {
		t.Fatalf("delete %v", got)
	}
	r := plan.Result
	if r.Phase != "Ready" || r.DataSource != "ubuntu" || r.SizeBytes != 10737418240 || r.Digest != "uid:abc" || len(r.Versions) != 1 || r.Versions[0].DataSource != golden.GetName() {
		t.Fatalf("result %+v", r)
	}
	if plan.Requeue != 0 {
		t.Fatalf("requeue %v", plan.Requeue)
	}
}

func TestDecideVolumeWaitsForTheDataSource(t *testing.T) {
	s := httpSubject()
	world := World{
		Snapshots:   []unstructured.Unstructured{goldenSnapshot(s, "uid:abc", true, time.Minute)},
		DataSources: []unstructured.Unstructured{dataSourceNamed("ubuntu", false)},
		Prev:        v1alpha1.ImageStatus{DataSource: "previous"},
	}
	plan := Decide(s, world, testNow)
	if plan.Result.Phase != "Importing" || plan.Result.Reason != "WaitingForDataSource" || plan.Result.DataSource != "previous" {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideVolumeReplacesAStaleDataVolume(t *testing.T) {
	s := httpSubject()
	stale := ImportDataVolume(ubuntuSubject(httpSpec("https://example.com/old.qcow2")), 10)
	plan := Decide(s, World{DataVolume: stale}, testNow)
	if got := namesAndKinds(plan.Delete); !slices.Equal(got, []string{"DataVolume/ubuntu-import"}) || len(plan.Apply) != 0 {
		t.Fatalf("plan %v %v", got, namesAndKinds(plan.Apply))
	}
	if plan.Result.Phase != "Pending" || plan.Result.Reason != "SourceChanged" || plan.Requeue != 2*time.Second {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideVolumeKeepsOldVersionsUpToKeep(t *testing.T) {
	s := httpSubject()
	older := func(url, digest string, age time.Duration) unstructured.Unstructured {
		return goldenSnapshot(ubuntuSubject(httpSpec(url)), digest, true, age)
	}
	current := goldenSnapshot(s, "uid:4", true, time.Minute)
	snaps := []unstructured.Unstructured{
		older("https://example.com/1", "uid:1", 4*time.Hour),
		older("https://example.com/2", "uid:2", 3*time.Hour),
		older("https://example.com/3", "uid:3", 2*time.Hour),
		current,
	}
	var prev []v1alpha1.ImageVersion
	for _, snap := range []unstructured.Unstructured{snaps[2], snaps[1], snaps[0]} {
		prev = append(prev, v1alpha1.ImageVersion{Digest: snap.GetAnnotations()[AnnotationDigest], DataSource: snap.GetName()})
	}
	world := World{
		Snapshots:   snaps,
		DataSources: []unstructured.Unstructured{dataSourceNamed("ubuntu", true), dataSourceNamed(snaps[0].GetName(), true)},
		Prev:        v1alpha1.ImageStatus{Versions: append([]v1alpha1.ImageVersion{{Digest: "uid:4", DataSource: current.GetName()}}, prev...)},
	}
	plan := Decide(s, world, testNow)
	wantDelete := []string{"DataSource/" + snaps[0].GetName(), "VolumeSnapshot/" + snaps[0].GetName()}
	slices.Sort(wantDelete)
	if got := namesAndKinds(plan.Delete); !slices.Equal(got, wantDelete) {
		t.Fatalf("delete %v, want %v", got, wantDelete)
	}
	if got := digests(plan.Result.Versions); !slices.Equal(got, []string{"uid:4", "uid:3", "uid:2"}) {
		t.Fatalf("versions %v", got)
	}
}

func TestDecideVolumeWaitsForTheVolumeRef(t *testing.T) {
	s := ubuntuSubject(v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{VolumeRef: &v1alpha1.ImageVolumeSource{Name: "web-root"}}})
	missing := Decide(s, World{}, testNow)
	if missing.Result.Reason != "VolumeNotFound" || missing.Result.Message != "claim web-root not found in acme-web" || len(missing.Apply) != 0 || missing.Requeue != 30*time.Second {
		t.Fatalf("missing %+v", missing)
	}

	plan := Decide(s, World{SourceClaim: claim("src", "12Gi")}, testNow)
	if got := nested(t, plan.Apply[0], "spec", "storage", "resources", "requests", "storage"); got != "12Gi" {
		t.Fatalf("size %v", got)
	}
}

func TestDecideVolumeUploadWaitsForUpload(t *testing.T) {
	s := ubuntuSubject(v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}})
	world := World{DataVolume: dataVolume(s, "UploadReady", ""), PlatformHost: "cloud.example.com"}
	plan := Decide(s, world, testNow)
	if plan.Result.Phase != "WaitingForUpload" || plan.Result.UploadURL != "https://upload.cloud.example.com/v1beta1/upload-async" || plan.Requeue != 30*time.Second {
		t.Fatalf("result %+v", plan.Result)
	}

	world.PlatformHost = ""
	plan = Decide(s, world, testNow)
	if plan.Result.Phase != "WaitingForUpload" || plan.Result.Reason != "PlatformHostNotSet" || plan.Result.UploadURL != "" {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideVolumeRefusesUploadOnAClusterImage(t *testing.T) {
	img := v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "catalog"}, Spec: v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}}}
	plan := Decide(SubjectOfClusterImage(img), World{}, testNow)
	if plan.Result.Phase != "Failed" || plan.Result.Reason != "UploadNotSupported" || len(plan.Apply) != 0 || len(plan.Delete) != 0 {
		t.Fatalf("plan %+v", plan)
	}
}

func TestDecideVolumeKeepsTheISOMarker(t *testing.T) {
	s := ubuntuSubject(v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeISO, Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: "https://example.com/a.iso"}}})
	worlds := []World{
		{},
		{DataVolume: dataVolume(s, "Succeeded", ""), ImportClaim: claim("abc", "")},
		{Snapshots: []unstructured.Unstructured{goldenSnapshot(s, "uid:abc", true, time.Minute)}},
	}
	for _, world := range worlds {
		plan := Decide(s, world, testNow)
		if len(plan.Apply) == 0 {
			t.Fatal("expected objects to apply")
		}
		for _, obj := range plan.Apply {
			if obj.GetAnnotations()[AnnotationAttach] != "read-only" {
				t.Fatalf("%s lacks the marker", obj.GetKind())
			}
		}
	}
}

func TestDecideKeepsThePreviousDataSourceWhileReimporting(t *testing.T) {
	s := httpSubject()
	prev := v1alpha1.ImageStatus{
		DataSource: "ubuntu",
		Digest:     "uid:old",
		SizeBytes:  5,
		Versions:   []v1alpha1.ImageVersion{{Digest: "uid:old", DataSource: "ubuntu-old"}},
	}
	stale := ImportDataVolume(ubuntuSubject(httpSpec("https://example.com/old.qcow2")), 10)
	plan := Decide(s, World{DataVolume: stale, Prev: prev}, testNow)
	r := plan.Result
	if r.Reason != "SourceChanged" || r.DataSource != "ubuntu" || r.Digest != "uid:old" || r.SizeBytes != 5 || len(r.Versions) != 1 {
		t.Fatalf("result %+v", r)
	}
}

func TestDecideVolumeTakesANewSnapshotForANewSourceHash(t *testing.T) {
	small := ubuntuSubject(httpSpec("https://example.com/a.qcow2"))
	bigSpec := httpSpec("https://example.com/a.qcow2")
	bigSpec.SizeGi = 20
	big := ubuntuSubject(bigSpec)
	existing := goldenSnapshot(small, "uid:same", true, time.Hour)

	world := World{
		Snapshots:   []unstructured.Unstructured{existing},
		DataVolume:  dataVolume(big, "Succeeded", ""),
		ImportClaim: claim("same", ""),
	}
	plan := Decide(big, world, testNow)
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, []string{"VolumeSnapshot/" + goldenNameOf(big, "uid:same")}) {
		t.Fatalf("apply %v", got)
	}
	if goldenNameOf(big, "uid:same") == existing.GetName() {
		t.Fatal("the new snapshot must not reuse the old name")
	}
}
