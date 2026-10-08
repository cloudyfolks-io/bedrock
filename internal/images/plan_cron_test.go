package images

import (
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func cronSubject() Subject {
	spec := registrySpec("quay.io/containerdisks/ubuntu:24.04")
	spec.Refresh = &v1alpha1.ImageRefresh{Schedule: "0 3 * * *", Keep: 2}
	return ubuntuSubject(spec)
}

func cronWithStatus(dataVolume, digest string, conditions ...map[string]any) *unstructured.Unstructured {
	cron := cronIn("", conditions...)
	if dataVolume != "" {
		status := cron.Object["status"].(map[string]any)
		status["currentImports"] = []any{map[string]any{"DataVolumeName": dataVolume, "Digest": digest}}
		status["lastImportTimestamp"] = "2026-10-08T03:00:00Z"
	}
	return cron
}

func namedSnapshot(name string, ready bool) unstructured.Unstructured {
	snap := namedObject("VolumeSnapshot", name)
	snap.Object["status"] = map[string]any{"readyToUse": ready, "restoreSize": "10Gi"}
	return snap
}

func TestDecideCronAppliesTheCron(t *testing.T) {
	plan := Decide(cronSubject(), World{}, testNow)
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, []string{"DataImportCron/ubuntu"}) {
		t.Fatalf("apply %v", got)
	}
	if plan.Result.Phase != "Pending" || plan.Result.Reason != "ImportScheduled" {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideCronReadyWhenTheLatestImportIsSnapshotted(t *testing.T) {
	world := World{
		Cron:        cronWithStatus("ubuntu-1a2b3c4d5e6f", "sha256:1a2b3c4d5e6f0000", condition("UpToDate", "True", "")),
		Snapshots:   []unstructured.Unstructured{namedSnapshot("ubuntu-1a2b3c4d5e6f", true)},
		DataSources: []unstructured.Unstructured{dataSourceNamed("ubuntu", true)},
	}
	plan := Decide(cronSubject(), world, testNow)
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, []string{"DataImportCron/ubuntu", "DataSource/ubuntu-1a2b3c4d5e6f"}) {
		t.Fatalf("apply %v", got)
	}
	r := plan.Result
	if r.Phase != "Ready" || r.Digest != "sha256:1a2b3c4d5e6f0000" || r.SizeBytes != 10737418240 || r.DataSource != "ubuntu" {
		t.Fatalf("result %+v", r)
	}
	if len(r.Versions) != 1 || r.Versions[0].DataSource != "ubuntu-1a2b3c4d5e6f" || r.Versions[0].Time.UTC().Hour() != 3 {
		t.Fatalf("versions %+v", r.Versions)
	}
}

func TestDecideCronKeepsPinnedDataSourcesForKeptVersions(t *testing.T) {
	world := World{
		Cron: cronWithStatus("ubuntu-bbbbbbbbbbbb", "sha256:b", condition("UpToDate", "True", "")),
		Snapshots: []unstructured.Unstructured{
			namedSnapshot("ubuntu-bbbbbbbbbbbb", true),
			namedSnapshot("ubuntu-aaaaaaaaaaaa", true),
		},
		DataSources: []unstructured.Unstructured{
			dataSourceNamed("ubuntu", true),
			dataSourceNamed("ubuntu-aaaaaaaaaaaa", true),
			dataSourceNamed("ubuntu-dead00000000", true),
		},
		Prev: v1alpha1.ImageStatus{Versions: []v1alpha1.ImageVersion{
			{Digest: "sha256:gone", DataSource: "ubuntu-dead00000000"},
			{Digest: "sha256:a", DataSource: "ubuntu-aaaaaaaaaaaa"},
		}},
	}
	plan := Decide(cronSubject(), world, testNow)
	if got := digests(plan.Result.Versions); !slices.Equal(got, []string{"sha256:b", "sha256:a"}) {
		t.Fatalf("versions %v", got)
	}
	if got := namesAndKinds(plan.Delete); !slices.Equal(got, []string{"DataSource/ubuntu-dead00000000"}) {
		t.Fatalf("delete %v", got)
	}
}

func TestDecideCronPendingWithoutImport(t *testing.T) {
	world := World{Cron: cronWithStatus("", "", condition("UpToDate", "False", "No source digest"))}
	plan := Decide(cronSubject(), world, testNow)
	if plan.Result.Phase != "Pending" || plan.Result.Message != "No source digest" {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideCronSnapshotFormatUnavailable(t *testing.T) {
	cron := cronIn("pvc")
	plan := Decide(cronSubject(), World{Cron: cron}, testNow)
	if plan.Result.Phase != "Pending" || plan.Result.Reason != "SnapshotFormatUnavailable" {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideCronDeletesTheImportDataVolume(t *testing.T) {
	s := cronSubject()
	plan := Decide(s, World{DataVolume: dataVolume(s, "Succeeded", "")}, testNow)
	if got := namesAndKinds(plan.Delete); !slices.Equal(got, []string{"DataVolume/ubuntu-import"}) {
		t.Fatalf("delete %v", got)
	}
	if got := namesAndKinds(plan.Apply); !slices.Equal(got, []string{"DataImportCron/ubuntu"}) {
		t.Fatalf("apply %v", got)
	}
}

func TestDecideCronReportsProgressOfTheCronImport(t *testing.T) {
	s := cronSubject()
	running := namedObject("DataVolume", "ubuntu-1a2b3c4d5e6f")
	running.Object["status"] = map[string]any{"phase": "ImportInProgress", "progress": "17%"}
	world := World{
		Cron:       cronWithStatus("ubuntu-1a2b3c4d5e6f", "sha256:1a2b", condition("Progressing", "True", "")),
		DataVolume: &running,
	}
	if plan := Decide(s, world, testNow); plan.Result.Phase != "Importing" || plan.Result.Progress != "17%" {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideVolumeDeletesTheCron(t *testing.T) {
	cron := namedObject("DataImportCron", "ubuntu")
	plan := Decide(httpSubject(), World{Cron: &cron}, testNow)
	if got := namesAndKinds(plan.Delete); !slices.Equal(got, []string{"DataImportCron/ubuntu"}) || len(plan.Apply) != 0 {
		t.Fatalf("plan %v %v", got, namesAndKinds(plan.Apply))
	}
	if plan.Result.Phase != "Pending" || plan.Requeue != 2*time.Second {
		t.Fatalf("result %+v", plan.Result)
	}
}

func TestDecideDispatchesByUsesCron(t *testing.T) {
	scheduled := namesAndKinds(Decide(cronSubject(), World{}, testNow).Apply)
	plain := namesAndKinds(Decide(ubuntuSubject(registrySpec("quay.io/containerdisks/ubuntu:24.04")), World{}, testNow).Apply)
	if !slices.Equal(scheduled, []string{"DataImportCron/ubuntu"}) || !slices.Equal(plain, []string{"DataVolume/ubuntu-import"}) {
		t.Fatalf("scheduled %v plain %v", scheduled, plain)
	}
}
