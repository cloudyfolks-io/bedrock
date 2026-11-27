package database

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func healthy() Observation {
	return Observation{Bootstrapped: true, ReadyBefore: true, EngineReady: true, GateReady: true, EndpointReady: true, Age: time.Hour, ProvisionTimeout: 30 * time.Minute}
}

func TestPhaseTable(t *testing.T) {
	edit := func(change func(*Observation)) Observation {
		o := healthy()
		change(&o)
		return o
	}
	cases := []struct {
		name   string
		in     Observation
		phase  string
		reason string
	}{
		{"everything ready", healthy(), "Ready", ReasonReady},
		{"deleting wins over everything", edit(func(o *Observation) { o.Deleting = true; o.Suspended = true; o.EngineReady = false }), "Deleting", ReasonDeleting},
		{"suspended", edit(func(o *Observation) { o.Suspended = true; o.EngineReady = false }), "Suspended", ReasonSuspended},
		{"new and waiting for the engine", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore = false, false
			o.EngineReady = false
			o.Age = time.Minute
		}), "Provisioning", ReasonEngineWaiting},
		{"new and waiting for the bootstrap", edit(func(o *Observation) { o.Bootstrapped, o.ReadyBefore = false, false; o.Age = time.Minute }), "Provisioning", ReasonBootstrapping},
		{"engine ready but the bootstrap is missing, gate and proxy up", edit(func(o *Observation) { o.Bootstrapped, o.ReadyBefore = false, false; o.Age = time.Minute }), "Provisioning", ReasonBootstrapping},
		{"restore in progress", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore = false, false
			o.EngineReady = false
			o.Restoring = true
			o.Age = time.Minute
		}), "Restoring", ReasonEngineWaiting},
		{"restore finished", edit(func(o *Observation) { o.Restoring = true }), "Ready", ReasonReady},
		{"restore over the timeout", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore = false, false
			o.EngineReady = false
			o.Restoring = true
			o.Age = 2 * time.Hour
		}), "Failed", ReasonEngineWaiting},
		{"provisioning over the timeout", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore = false, false
			o.EngineReady = false
			o.Age = 31 * time.Minute
		}), "Failed", ReasonEngineWaiting},
		{"provisioning at the timeout", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore = false, false
			o.EngineReady = false
			o.Age = 30 * time.Minute
		}), "Provisioning", ReasonEngineWaiting},
		{"no timeout configured", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore = false, false
			o.EngineReady = false
			o.Age = 100 * time.Hour
			o.ProvisionTimeout = 0
		}), "Provisioning", ReasonEngineWaiting},
		{"ready before, engine down now", edit(func(o *Observation) { o.EngineReady = false; o.Age = 100 * time.Hour }), "Unhealthy", ReasonEngineWaiting},
		{"ready before, gate down now", edit(func(o *Observation) { o.GateReady = false }), "Unhealthy", ReasonGateWaiting},
		{"ready before, proxy down now", edit(func(o *Observation) { o.EndpointReady = false }), "Unhealthy", ReasonProxyWaiting},
		{"a refusal while new", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore, o.EngineReady, o.Age = false, false, false, time.Minute
			o.Refusal = &Refusal{Reason: ReasonCapacityExceeded, Message: "full"}
		}), "Provisioning", ReasonCapacityExceeded},
		{"a refusal over the timeout", edit(func(o *Observation) {
			o.Bootstrapped, o.ReadyBefore, o.EngineReady, o.Age = false, false, false, time.Hour
			o.Refusal = &Refusal{Reason: ReasonRestoreSourceEmpty, Message: "no backups"}
		}), "Failed", ReasonRestoreSourceEmpty},
	}
	for _, c := range cases {
		got := Phase(c.in)
		if got.Phase != c.phase || got.Reason != c.reason || got.Message == "" {
			t.Errorf("%s: %+v, want %s %s", c.name, got, c.phase, c.reason)
		}
	}
}

func TestFailedKeepsTheLastReasonAndNamesTheTimeout(t *testing.T) {
	o := healthy()
	o.Bootstrapped, o.ReadyBefore, o.EngineReady, o.EngineReason, o.Age = false, false, false, "Setting up primary", time.Hour
	got := Phase(o)
	if got.Phase != "Failed" || got.Message != "the database was not ready after 30m0s: Setting up primary" {
		t.Fatalf("verdict %+v", got)
	}
}

func TestBackupHealth(t *testing.T) {
	now := mustTime("2026-10-19T12:00:00Z")
	at := func(hours int) *v1alpha1.DatabaseBackupStatus {
		last := metav1.NewTime(now.Add(-time.Duration(hours) * time.Hour))
		return &v1alpha1.DatabaseBackupStatus{LastAt: &last, Items: []v1alpha1.DatabaseBackupItem{{ID: "b", Phase: "completed"}}}
	}
	failed := at(1)
	failed.Items = []v1alpha1.DatabaseBackupItem{{ID: "bad", Phase: "failed"}, {ID: "b", Phase: "completed"}}
	cases := []struct {
		name    string
		in      BackupObservation
		healthy bool
		unknown bool
		reason  string
	}{
		{"off", BackupObservation{Now: now}, true, false, "BackupDisabled"},
		{"on and current", BackupObservation{Enabled: true, Now: now, Status: at(10), Age: 100 * time.Hour}, true, false, "BackupCurrent"},
		{"on and stale", BackupObservation{Enabled: true, Now: now, Status: at(37), Age: 100 * time.Hour}, false, false, "BackupStale"},
		{"on at the limit", BackupObservation{Enabled: true, Now: now, Status: at(36), Age: 100 * time.Hour}, true, false, "BackupCurrent"},
		{"newest failed", BackupObservation{Enabled: true, Now: now, Status: failed, Age: time.Hour}, false, false, "BackupFailed"},
		{"new and no backup", BackupObservation{Enabled: true, Now: now, Age: time.Hour}, false, true, "NoBackupYet"},
		{"old and no backup", BackupObservation{Enabled: true, Now: now, Age: 40 * time.Hour}, false, false, "BackupStale"},
		{"empty status", BackupObservation{Enabled: true, Now: now, Status: &v1alpha1.DatabaseBackupStatus{}, Age: time.Hour}, false, true, "NoBackupYet"},
	}
	for _, c := range cases {
		got := BackupHealth(c.in)
		if got.Healthy != c.healthy || got.Unknown != c.unknown || got.Reason != c.reason || got.Message == "" {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
