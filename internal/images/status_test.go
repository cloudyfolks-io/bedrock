package images

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func dataVolumeIn(phase, progress string, conditions ...map[string]any) *unstructured.Unstructured {
	items := make([]any, 0, len(conditions))
	for _, c := range conditions {
		items = append(items, c)
	}
	status := map[string]any{"phase": phase, "conditions": items}
	if progress != "" {
		status["progress"] = progress
	}
	return &unstructured.Unstructured{Object: map[string]any{"status": status}}
}

func condition(conditionType, status, message string) map[string]any {
	return map[string]any{"type": conditionType, "status": status, "message": message}
}

func TestPhaseOfDataVolume(t *testing.T) {
	cases := []struct {
		phase  string
		upload bool
		want   string
		reason string
	}{
		{"Failed", false, "Failed", "ImportFailed"},
		{"UploadReady", true, "WaitingForUpload", "WaitingForUpload"},
		{"UploadReady", false, "Pending", "ImportScheduled"},
		{"UploadScheduled", true, "Pending", "ImportScheduled"},
		{"Succeeded", false, "Importing", "Snapshotting"},
		{"ImportInProgress", false, "Importing", "Importing"},
		{"CloneInProgress", false, "Importing", "Importing"},
		{"SnapshotForSmartCloneInProgress", false, "Importing", "Importing"},
		{"SmartClonePVCInProgress", false, "Importing", "Importing"},
		{"CloneFromSnapshotSourceInProgress", false, "Importing", "Importing"},
		{"CSICloneInProgress", false, "Importing", "Importing"},
		{"ExpansionInProgress", false, "Importing", "Importing"},
		{"NamespaceTransferInProgress", false, "Importing", "Importing"},
		{"PrepClaimInProgress", false, "Importing", "Importing"},
		{"RebindInProgress", false, "Importing", "Importing"},
		{"", false, "Pending", "ImportScheduled"},
		{"Pending", false, "Pending", "ImportScheduled"},
		{"PVCBound", false, "Pending", "ImportScheduled"},
		{"ImportScheduled", false, "Pending", "ImportScheduled"},
		{"CloneScheduled", false, "Pending", "ImportScheduled"},
		{"WaitForFirstConsumer", false, "Pending", "ImportScheduled"},
		{"PendingPopulation", false, "Pending", "ImportScheduled"},
		{"Paused", false, "Pending", "ImportScheduled"},
		{"Unknown", false, "Pending", "ImportScheduled"},
	}
	for _, tc := range cases {
		dv := dataVolumeIn(tc.phase, "")
		if tc.upload {
			dv.Object["spec"] = map[string]any{"source": map[string]any{"upload": map[string]any{}}}
		}
		got := PhaseOfDataVolume(dv)
		if got.Phase != tc.want || got.Reason != tc.reason {
			t.Errorf("%q upload=%v: got %s/%s, want %s/%s", tc.phase, tc.upload, got.Phase, got.Reason, tc.want, tc.reason)
		}
	}
}

func TestPhaseOfDataVolumeMessageAndProgress(t *testing.T) {
	failed := dataVolumeIn("Failed", "N/A", condition("Bound", "True", "bound"), condition("Running", "False", "pull denied"))
	got := PhaseOfDataVolume(failed)
	if got.Message != "pull denied" || got.Progress != "" {
		t.Fatalf("result %+v", got)
	}
	if got := PhaseOfDataVolume(dataVolumeIn("ImportInProgress", "42.5%")); got.Progress != "42.5%" {
		t.Fatalf("progress %q", got.Progress)
	}
	if got := PhaseOfDataVolume(dataVolumeIn("ImportInProgress", "")); got.Progress != "" {
		t.Fatalf("progress %q", got.Progress)
	}
}

func cronWithImport(key string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"currentImports": []any{map[string]any{key + "ataVolumeName": "ubuntu-1a2b3c4d5e6f", key + "igest": "sha256:1a2b3c4d5e6f0000"}},
	}}}
}

func TestCronImportReadsCapitalisedFields(t *testing.T) {
	name, digest, ok := CronImport(cronWithImport("D"))
	if !ok || name != "ubuntu-1a2b3c4d5e6f" || digest != "sha256:1a2b3c4d5e6f0000" {
		t.Fatalf("import %q %q %v", name, digest, ok)
	}
	if _, _, ok := CronImport(cronWithImport("d")); ok {
		t.Fatal("lower case keys must not match")
	}
	if _, _, ok := CronImport(&unstructured.Unstructured{Object: map[string]any{}}); ok {
		t.Fatal("a cron without imports has none")
	}
}

func cronIn(sourceFormat string, conditions ...map[string]any) *unstructured.Unstructured {
	items := make([]any, 0, len(conditions))
	for _, c := range conditions {
		items = append(items, c)
	}
	status := map[string]any{"conditions": items}
	if sourceFormat != "" {
		status["sourceFormat"] = sourceFormat
	}
	return &unstructured.Unstructured{Object: map[string]any{"status": status}}
}

func readySnapshot(ready bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"readyToUse": ready}}}
}

func TestPhaseOfCron(t *testing.T) {
	aliasReady := dataVolumeIn("", "", condition("Ready", "True", ""))
	aliasNotReady := dataVolumeIn("", "", condition("Ready", "False", ""))
	cases := map[string]struct {
		cron, alias, snapshot *unstructured.Unstructured
		phase, reason         string
	}{
		"ready":            {cronIn("", condition("UpToDate", "True", "")), aliasReady, readySnapshot(true), "Ready", "Imported"},
		"waiting for data": {cronIn("", condition("UpToDate", "True", "")), aliasNotReady, readySnapshot(true), "Importing", "WaitingForDataSource"},
		"progressing":      {cronIn("", condition("Progressing", "True", "")), nil, nil, "Importing", "Importing"},
		"pvc format":       {cronIn("pvc"), nil, nil, "Pending", "SnapshotFormatUnavailable"},
		"new cron":         {cronIn(""), nil, nil, "Pending", "ImportScheduled"},
		"no cron":          {nil, nil, nil, "Pending", "ImportScheduled"},
	}
	for name, tc := range cases {
		got := PhaseOfCron(tc.cron, tc.alias, tc.snapshot)
		if got.Phase != tc.phase || got.Reason != tc.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", name, got.Phase, got.Reason, tc.phase, tc.reason)
		}
	}
	pending := PhaseOfCron(cronIn("", condition("UpToDate", "False", "No source digest")), nil, nil)
	if pending.Message != "No source digest" {
		t.Fatalf("message %q", pending.Message)
	}
}

func TestDigestOfClaim(t *testing.T) {
	claim := &corev1.PersistentVolumeClaim{}
	claim.UID = types.UID("abc")
	cases := map[string]struct {
		spec  v1alpha1.ImageSpec
		claim *corev1.PersistentVolumeClaim
		want  string
	}{
		"pinned registry": {registrySpec(cirrosRef), claim, "sha256:ebdb8d8b9b480f6ee7664ed3fdde8428767664f507d98f94090edeff04d7ebf2"},
		"tag registry":    {registrySpec("r/i:1"), claim, "uid:abc"},
		"http sha256":     {v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: "https://e/a", Checksum: "sha256:fff"}}}, claim, "sha256:fff"},
		"http md5":        {v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{HTTP: &v1alpha1.ImageHTTPSource{URL: "https://e/a", Checksum: "md5:fff"}}}, claim, "uid:abc"},
		"upload":          {v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{Upload: &v1alpha1.ImageUploadSource{}}}, claim, "uid:abc"},
		"volume":          {v1alpha1.ImageSpec{Source: v1alpha1.ImageSource{VolumeRef: &v1alpha1.ImageVolumeSource{Name: "v"}}}, claim, "uid:abc"},
		"no claim":        {registrySpec(cirrosRef), nil, ""},
	}
	for name, tc := range cases {
		if got := DigestOfClaim(tc.spec, tc.claim); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestSnapshotReadyAndRestoreBytes(t *testing.T) {
	snapshot := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"readyToUse": true, "restoreSize": "10Gi"}}}
	if !SnapshotReady(snapshot) || RestoreBytes(snapshot) != 10737418240 {
		t.Fatalf("ready %v bytes %d", SnapshotReady(snapshot), RestoreBytes(snapshot))
	}
	empty := &unstructured.Unstructured{Object: map[string]any{}}
	if SnapshotReady(empty) || RestoreBytes(empty) != 0 {
		t.Fatal("an empty snapshot is not ready and has no size")
	}
}

func TestBuildStatusReadyCondition(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	prev := v1alpha1.ImageStatus{Phase: "Pending", DataSource: "old"}

	ready := BuildStatus(prev, 4, Result{Phase: "Ready", DataSource: "ubuntu", Digest: "sha:a", SizeBytes: 7, Progress: "100%"}, now)
	cond := ready.Conditions[0]
	if cond.Type != "Ready" || cond.Status != metav1.ConditionTrue || cond.Reason != "Imported" || cond.ObservedGeneration != 4 {
		t.Fatalf("condition %+v", cond)
	}
	if ready.ObservedGeneration != 4 || ready.DataSource != "ubuntu" || ready.Digest != "sha:a" || ready.SizeBytes != 7 || ready.Progress != "100%" {
		t.Fatalf("status %+v", ready)
	}

	pending := BuildStatus(prev, 4, Result{Phase: "Pending", Reason: "ImportScheduled", Message: "waiting"}, now)
	cond = pending.Conditions[0]
	if cond.Status != metav1.ConditionFalse || cond.Reason != "ImportScheduled" || cond.Message != "waiting" {
		t.Fatalf("condition %+v", cond)
	}
	bare := BuildStatus(prev, 4, Result{Phase: "Importing"}, now)
	if bare.Conditions[0].Reason != "Importing" {
		t.Fatalf("reason %q", bare.Conditions[0].Reason)
	}

	if prev.Phase != "Pending" || prev.DataSource != "old" || len(prev.Conditions) != 0 {
		t.Fatalf("the input was changed: %+v", prev)
	}
}
