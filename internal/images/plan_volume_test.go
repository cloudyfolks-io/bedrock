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

func digests(versions []v1alpha1.ImageVersion) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		out = append(out, v.Digest)
	}
	return out
}

func snapshotWithSource(s Subject, label string, age time.Duration) unstructured.Unstructured {
	other := ubuntuSubject(httpSpec("https://example.com/" + label))
	other.Owner = s.Owner
	return goldenSnapshot(other, "uid:"+label, true, age)
}

func snapshotDataSource(name, snapshot string) unstructured.Unstructured {
	ds := namedObject("DataSource", name)
	ds.Object["spec"] = map[string]any{"source": map[string]any{"snapshot": map[string]any{"name": snapshot, "namespace": "acme-web"}}}
	return ds
}

type retentionWorld struct {
	s         Subject
	snapshots []unstructured.Unstructured
	current   unstructured.Unstructured
}

func newRetentionWorld() retentionWorld {
	s := httpSubject()
	return retentionWorld{
		s: s,
		snapshots: []unstructured.Unstructured{
			snapshotWithSource(s, "one", 5*time.Hour),
			snapshotWithSource(s, "two", 4*time.Hour),
			snapshotWithSource(s, "three", 3*time.Hour),
			snapshotWithSource(s, "four", 2*time.Hour),
			goldenSnapshot(s, "uid:current", true, time.Minute),
		},
	}
}

func aliasPointingAt(snapshot string) unstructured.Unstructured {
	alias := dataSourceNamed("ubuntu", true)
	alias.Object["spec"] = map[string]any{"source": map[string]any{"snapshot": map[string]any{"name": snapshot, "namespace": "acme-web"}}}
	return alias
}

func (r retentionWorld) world(extra ...func(*World)) World {
	alias := aliasPointingAt(r.snapshots[2].GetName())
	world := World{
		Snapshots:      r.snapshots,
		DataSources:    []unstructured.Unstructured{alias},
		AllDataSources: []unstructured.Unstructured{alias},
	}
	for _, mutate := range extra {
		mutate(&world)
	}
	return world
}

func deletedSnapshots(plan Plan) []string {
	var names []string
	for _, obj := range plan.Delete {
		if obj.GetKind() == "VolumeSnapshot" {
			names = append(names, obj.GetName())
		}
	}
	slices.Sort(names)
	return names
}

func TestDecideVolumeWithAnEmptyStatusPrunesOnlyWhatIsFree(t *testing.T) {
	r := newRetentionWorld()
	plan := Decide(r.s, r.world(), testNow)

	want := []string{r.snapshots[0].GetName(), r.snapshots[1].GetName()}
	slices.Sort(want)
	if got := deletedSnapshots(plan); !slices.Equal(got, want) {
		t.Fatalf("deleted %v, want %v", got, want)
	}
	for _, kept := range []int{2, 3, 4} {
		if slices.Contains(deletedSnapshots(plan), r.snapshots[kept].GetName()) {
			t.Fatalf("snapshot %s is in the keep set", r.snapshots[kept].GetName())
		}
	}
	if got := digests(plan.Result.Versions); !slices.Equal(got, []string{"uid:current", "uid:four", "uid:three"}) {
		t.Fatalf("versions %v", got)
	}
}

func TestDecideVolumeKeepsASnapshotThatADataVolumeReferences(t *testing.T) {
	r := newRetentionWorld()
	consumer := namedObject("DataVolume", "vm-root")
	consumer.Object["spec"] = map[string]any{"source": map[string]any{"snapshot": map[string]any{"name": r.snapshots[1].GetName(), "namespace": "acme-web"}}}
	plan := Decide(r.s, r.world(func(w *World) { w.DataVolumes = []unstructured.Unstructured{consumer} }), testNow)

	if got := deletedSnapshots(plan); !slices.Equal(got, []string{r.snapshots[0].GetName()}) {
		t.Fatalf("deleted %v", got)
	}
	if plan.Requeue != 30*time.Second {
		t.Fatalf("a blocked delete must requeue: %v", plan.Requeue)
	}
}

func TestDecideVolumeKeepsASnapshotThatADataSourceReferences(t *testing.T) {
	r := newRetentionWorld()
	pinned := snapshotDataSource("pinned", r.snapshots[0].GetName())
	plan := Decide(r.s, r.world(func(w *World) { w.AllDataSources = append(w.AllDataSources, pinned) }), testNow)
	if got := deletedSnapshots(plan); !slices.Equal(got, []string{r.snapshots[1].GetName()}) {
		t.Fatalf("deleted %v", got)
	}
}

func TestDecideVolumeKeepsASnapshotThatAClaimReferences(t *testing.T) {
	r := newRetentionWorld()
	restored := claim("restored", "")
	restored.Namespace = "acme-web"
	restored.Spec.DataSource = &corev1.TypedLocalObjectReference{Kind: "VolumeSnapshot", Name: r.snapshots[0].GetName()}
	plan := Decide(r.s, r.world(func(w *World) { w.Claims = []corev1.PersistentVolumeClaim{*restored} }), testNow)
	if got := deletedSnapshots(plan); !slices.Equal(got, []string{r.snapshots[1].GetName()}) {
		t.Fatalf("deleted %v", got)
	}
}

func TestDecideVolumeDeletesTheVersionDataSourceBeforeItsSnapshot(t *testing.T) {
	r := newRetentionWorld()
	oldest := r.snapshots[0].GetName()
	version := snapshotDataSource(oldest, oldest)
	version.SetLabels(r.s.Labels)
	withVersion := func(w *World) {
		w.DataSources = append(w.DataSources, version)
		w.AllDataSources = append(w.AllDataSources, version)
	}

	first := Decide(r.s, r.world(withVersion), testNow)
	if got := deletedSnapshots(first); slices.Contains(got, oldest) {
		t.Fatalf("the snapshot must stay while its DataSource exists: %v", got)
	}
	if !slices.Contains(namesAndKinds(first.Delete), "DataSource/"+oldest) {
		t.Fatalf("the version DataSource must go first: %v", namesAndKinds(first.Delete))
	}

	second := Decide(r.s, r.world(), testNow)
	if !slices.Contains(deletedSnapshots(second), oldest) {
		t.Fatalf("the snapshot must go once nothing references it: %v", deletedSnapshots(second))
	}
}

func TestDecideVolumeNeverDeletesTheAliasTarget(t *testing.T) {
	r := newRetentionWorld()
	oldest := r.snapshots[0].GetName()
	plan := Decide(r.s, r.world(func(w *World) {
		alias := aliasPointingAt(oldest)
		w.DataSources = []unstructured.Unstructured{alias}
		w.AllDataSources = []unstructured.Unstructured{alias}
	}), testNow)
	if slices.Contains(deletedSnapshots(plan), oldest) || len(deletedSnapshots(plan)) != 0 {
		t.Fatalf("deleted %v", deletedSnapshots(plan))
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

func TestDecideVolumeReportsASnapshotError(t *testing.T) {
	s := httpSubject()
	broken := goldenSnapshot(s, "uid:abc", false, time.Minute)
	broken.Object["status"] = map[string]any{"readyToUse": false, "error": map[string]any{"message": "volume snapshot class missing"}}
	plan := Decide(s, World{Snapshots: []unstructured.Unstructured{broken}}, testNow)
	r := plan.Result
	if r.Phase != "Failed" || r.Reason != "SnapshotFailed" || r.Message != "volume snapshot class missing" {
		t.Fatalf("result %+v", r)
	}
	if plan.Requeue != 5*time.Minute {
		t.Fatalf("a bad snapshot must back off: %v", plan.Requeue)
	}
}
