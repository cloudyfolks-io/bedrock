package operator

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

func upgradeCluster(desired, running string, upgrade *v1alpha1.UpgradeStatus) v1alpha1.Cluster {
	return v1alpha1.Cluster{Spec: v1alpha1.ClusterSpec{DesiredVersion: desired}, Status: v1alpha1.ClusterStatus{Version: running, Upgrade: upgrade}}
}

func TestRoute(t *testing.T) {
	inFlight := &v1alpha1.UpgradeStatus{From: "v1", To: "v2", Attempt: 1}
	cases := map[string]struct {
		cluster  v1alpha1.Cluster
		embedded string
		want     string
	}{
		"first install":                     {upgradeCluster("v1", "", nil), "v1", routeInstall},
		"install of another version":        {upgradeCluster("v2", "", nil), "v1", routeAwait},
		"steady":                            {upgradeCluster("v1", "v1", nil), "v1", routeSteady},
		"steady on another operator":        {upgradeCluster("v1", "v1", nil), "v2", routeAwait},
		"old operator":                      {upgradeCluster("v2", "v1", nil), "v1", routeOld},
		"new operator":                      {upgradeCluster("v2", "v1", nil), "v2", routeNew},
		"third operator":                    {upgradeCluster("v2", "v1", nil), "v3", routeAwait},
		"old operator after a restore":      {upgradeCluster("v2", "v1", inFlight), "v1", routeOld},
		"desired set back during upgrade":   {upgradeCluster("v1", "v1", inFlight), "v1", routeOld},
		"new operator after desired change": {upgradeCluster("v3", "v1", inFlight), "v2", routeNew},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := route(tc.cluster, tc.embedded); got != tc.want {
				t.Fatalf("route = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNextPhase(t *testing.T) {
	want := map[string]string{
		v1alpha1.PhasePreflight:    v1alpha1.PhaseBackup,
		v1alpha1.PhaseBackup:       v1alpha1.PhasePreload,
		v1alpha1.PhasePreload:      v1alpha1.PhaseControlPlane,
		v1alpha1.PhaseControlPlane: v1alpha1.PhaseComponents,
		v1alpha1.PhaseComponents:   v1alpha1.PhaseWorkers,
		v1alpha1.PhaseWorkers:      v1alpha1.PhaseVerify,
		v1alpha1.PhaseVerify:       v1alpha1.PhaseIdle,
	}
	for phase, next := range want {
		if got := nextPhase(phase); got != next {
			t.Fatalf("nextPhase(%s) = %s, want %s", phase, got, next)
		}
	}
}

func TestDecide(t *testing.T) {
	running := func(phase string) v1alpha1.Cluster {
		cluster := upgradeCluster("v2", "v1", &v1alpha1.UpgradeStatus{From: "v1", To: "v2", Attempt: 1})
		cluster.Status.Phase = phase
		return cluster
	}
	failed := func(phase string) v1alpha1.Cluster {
		cluster := running(v1alpha1.PhaseFailed)
		cluster.Status.Upgrade.FailedPhase = phase
		return cluster
	}
	withAction := func(cluster v1alpha1.Cluster, action string) v1alpha1.Cluster {
		cluster.Spec.Upgrade.Action = action
		return cluster
	}
	idle := upgradeCluster("v2", "v1", nil)
	idle.Status.Phase = v1alpha1.PhaseIdle
	retargeted := running(v1alpha1.PhaseBackup)
	retargeted.Spec.DesiredVersion = "v3"
	abortedAt := func(generation int64) v1alpha1.Cluster {
		cluster := idle
		cluster.Generation = 5
		cluster.Status.Conditions = nil
		setCondition(&cluster.Status, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonAborted, "", generation)
		return cluster
	}
	blockedAt := func(generation int64) v1alpha1.Cluster {
		cluster := idle
		cluster.Generation = generation
		cluster.Status = blockUpgrade(inPhase(v1alpha1.PhasePreflight), "timeSynced: node-a clock not synced", 5)
		return cluster
	}
	abortedAfterABlock := blockedAt(5)
	abortedAfterABlock.Status = abortUpgrade(abortedAfterABlock.Status, "", 5)
	cases := map[string]struct {
		cluster v1alpha1.Cluster
		want    decision
	}{
		"start":                      {idle, decision{Kind: decisionStart}},
		"run":                        {running(v1alpha1.PhaseBackup), decision{Kind: decisionRun}},
		"failed waits":               {failed(v1alpha1.PhaseBackup), decision{Kind: decisionWait}},
		"resume":                     {withAction(failed(v1alpha1.PhaseWorkers), v1alpha1.UpgradeActionResume), decision{Kind: decisionResume}},
		"resume while running":       {withAction(running(v1alpha1.PhaseBackup), v1alpha1.UpgradeActionResume), decision{Kind: decisionIgnore, Message: "resume ignored: phase Backup has not failed"}},
		"resume before a start":      {withAction(idle, v1alpha1.UpgradeActionResume), decision{Kind: decisionIgnore, Message: "resume ignored: phase Idle has not failed"}},
		"abort in Components":        {withAction(running(v1alpha1.PhaseComponents), v1alpha1.UpgradeActionAbort), decision{Kind: decisionRefuseAbort, Message: "abort refused in Components: no rollback after Components started; resume continues the upgrade"}},
		"abort after Workers failed": {withAction(failed(v1alpha1.PhaseWorkers), v1alpha1.UpgradeActionAbort), decision{Kind: decisionRefuseAbort, Message: "abort refused in Workers: no rollback after Components started; resume continues the upgrade"}},
		"abort in Verify":            {withAction(running(v1alpha1.PhaseVerify), v1alpha1.UpgradeActionAbort), decision{Kind: decisionRefuseAbort, Message: "abort refused in Verify: no rollback after Components started; resume continues the upgrade"}},
		"desired changed":            {retargeted, decision{Kind: decisionRetarget, Message: "desiredVersion is v3 while the upgrade to v2 runs: set it back to v2, or abort"}},
		"abort without its patch":    {abortedAt(5), decision{Kind: decisionEndAbort}},
		"new start after an abort":   {abortedAt(4), decision{Kind: decisionStart}},
		"blocked waits":              {blockedAt(5), decision{Kind: decisionWait}},
		"blocked resumes":            {withAction(blockedAt(6), v1alpha1.UpgradeActionResume), decision{Kind: decisionRetry}},
		"blocked with a new target":  {blockedAt(6), decision{Kind: decisionStart}},
		"aborted after a block":      {abortedAfterABlock, decision{Kind: decisionEndAbort}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := decide(tc.cluster); got != tc.want {
				t.Fatalf("decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func conditionOf(status v1alpha1.ClusterStatus, conditionType string) metav1.Condition {
	for _, condition := range status.Conditions {
		if condition.Type == conditionType {
			return condition
		}
	}
	return metav1.Condition{}
}

func TestStartUpgrade(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	before := v1alpha1.ClusterStatus{Version: "v1", Phase: v1alpha1.PhaseIdle}
	got := startUpgrade(before, "v2", now, 4)
	want := &v1alpha1.UpgradeStatus{From: "v1", To: "v2", StartedAt: now, PhaseStartedAt: now, Attempt: 1}
	if got.Phase != v1alpha1.PhasePreflight || !reflect.DeepEqual(got.Upgrade, want) {
		t.Fatalf("status %+v upgrade %+v", got, got.Upgrade)
	}
	progressing := conditionOf(got, v1alpha1.ConditionProgressing)
	if progressing.Status != metav1.ConditionTrue || progressing.Reason != v1alpha1.ReasonUpgrading || progressing.ObservedGeneration != 4 {
		t.Fatalf("progressing %+v", progressing)
	}
	if before.Upgrade != nil || before.Phase != v1alpha1.PhaseIdle {
		t.Fatalf("input changed: %+v", before)
	}
}

func inPhase(phase string) v1alpha1.ClusterStatus {
	start := metav1.NewTime(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	return v1alpha1.ClusterStatus{Version: "v1", Phase: phase, Upgrade: &v1alpha1.UpgradeStatus{From: "v1", To: "v2", StartedAt: start, PhaseStartedAt: start, Attempt: 1, Message: "working"}}
}

func TestAdvance(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
	t.Run("message", func(t *testing.T) {
		got := advance(inPhase(v1alpha1.PhasePreload), v1alpha1.PhasePreload, phaseResult{Message: "preload 1/3 nodes"}, now, 2)
		if got.Phase != v1alpha1.PhasePreload || got.Upgrade.Message != "preload 1/3 nodes" || got.Upgrade.PhaseStartedAt.Equal(&now) {
			t.Fatalf("status %+v upgrade %+v", got, got.Upgrade)
		}
	})
	t.Run("done enters the next phase", func(t *testing.T) {
		got := advance(inPhase(v1alpha1.PhaseBackup), v1alpha1.PhaseBackup, phaseResult{Done: true}, now, 2)
		if got.Phase != v1alpha1.PhasePreload || got.Upgrade.Message != "" || !got.Upgrade.PhaseStartedAt.Equal(&now) {
			t.Fatalf("status %+v upgrade %+v", got, got.Upgrade)
		}
	})
	t.Run("preflight done clears the block", func(t *testing.T) {
		blocked := inPhase(v1alpha1.PhasePreflight)
		blocked.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionUpgradeBlocked, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonBlocked}}
		got := advance(blocked, v1alpha1.PhasePreflight, phaseResult{Done: true}, now, 2)
		blockedCondition := conditionOf(got, v1alpha1.ConditionUpgradeBlocked)
		if got.Phase != v1alpha1.PhaseBackup || blockedCondition.Status != metav1.ConditionFalse || blockedCondition.Reason != "Passed" {
			t.Fatalf("phase %s blocked %+v", got.Phase, blockedCondition)
		}
	})
	t.Run("verify done finishes", func(t *testing.T) {
		got := advance(inPhase(v1alpha1.PhaseVerify), v1alpha1.PhaseVerify, phaseResult{Done: true}, now, 2)
		available := conditionOf(got, v1alpha1.ConditionAvailable)
		progressing := conditionOf(got, v1alpha1.ConditionProgressing)
		if got.Phase != v1alpha1.PhaseIdle || got.Version != "v2" || got.Upgrade != nil {
			t.Fatalf("status %+v", got)
		}
		if available.Status != metav1.ConditionTrue || available.Reason != v1alpha1.ReasonUpgraded || progressing.Status != metav1.ConditionFalse || progressing.Reason != v1alpha1.ReasonUpgraded {
			t.Fatalf("available %+v progressing %+v", available, progressing)
		}
	})
	t.Run("failure", func(t *testing.T) {
		got := advance(inPhase(v1alpha1.PhaseWorkers), v1alpha1.PhaseWorkers, phaseResult{Failure: "node-b: reboot failed"}, now, 2)
		progressing := conditionOf(got, v1alpha1.ConditionProgressing)
		if got.Phase != v1alpha1.PhaseFailed || got.Upgrade.FailedPhase != v1alpha1.PhaseWorkers || got.Upgrade.Message != "node-b: reboot failed" {
			t.Fatalf("status %+v upgrade %+v", got, got.Upgrade)
		}
		if progressing.Status != metav1.ConditionFalse || progressing.Reason != v1alpha1.ReasonPhaseFailed || progressing.Message != "node-b: reboot failed" {
			t.Fatalf("progressing %+v", progressing)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		got := advance(inPhase(v1alpha1.PhasePreflight), v1alpha1.PhasePreflight, phaseResult{Blocked: "time: node-a clock not synced"}, now, 2)
		blocked := conditionOf(got, v1alpha1.ConditionUpgradeBlocked)
		progressing := conditionOf(got, v1alpha1.ConditionProgressing)
		if got.Phase != v1alpha1.PhaseIdle || got.Upgrade != nil || got.Version != "v1" {
			t.Fatalf("status %+v", got)
		}
		if blocked.Status != metav1.ConditionTrue || blocked.Reason != v1alpha1.ReasonBlocked || blocked.Message != "time: node-a clock not synced" || progressing.Status != metav1.ConditionFalse || progressing.Reason != v1alpha1.ReasonBlocked {
			t.Fatalf("blocked %+v progressing %+v", blocked, progressing)
		}
	})
	t.Run("a stale result is dropped", func(t *testing.T) {
		moved := inPhase(v1alpha1.PhaseComponents)
		if got := advance(moved, v1alpha1.PhaseControlPlane, phaseResult{Done: true}, now, 2); !reflect.DeepEqual(got, moved) {
			t.Fatalf("status %+v", got)
		}
		finished := v1alpha1.ClusterStatus{Version: "v2", Phase: v1alpha1.PhaseIdle}
		if got := advance(finished, v1alpha1.PhaseVerify, phaseResult{Done: true}, now, 2); !reflect.DeepEqual(got, finished) {
			t.Fatalf("status %+v", got)
		}
	})
	t.Run("input unchanged", func(t *testing.T) {
		before := inPhase(v1alpha1.PhaseVerify)
		_ = advance(before, v1alpha1.PhaseVerify, phaseResult{Done: true}, now, 2)
		if before.Upgrade == nil || before.Phase != v1alpha1.PhaseVerify || before.Upgrade.Message != "working" {
			t.Fatalf("input changed: %+v", before)
		}
	})
}

func TestResumeUpgrade(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	failedStatus := advance(inPhase(v1alpha1.PhaseComponents), v1alpha1.PhaseComponents, phaseResult{Failure: "group fabric: timeout"}, now, 2)
	got := resumeUpgrade(failedStatus, now, 3)
	progressing := conditionOf(got, v1alpha1.ConditionProgressing)
	if got.Phase != v1alpha1.PhaseComponents || got.Upgrade.Attempt != 2 || got.Upgrade.FailedPhase != "" || got.Upgrade.Message != "" || !got.Upgrade.PhaseStartedAt.Equal(&now) {
		t.Fatalf("status %+v upgrade %+v", got, got.Upgrade)
	}
	if progressing.Status != metav1.ConditionTrue || progressing.Reason != v1alpha1.ReasonUpgrading || progressing.ObservedGeneration != 3 {
		t.Fatalf("progressing %+v", progressing)
	}
	if failedStatus.Upgrade.Attempt != 1 {
		t.Fatal("input changed")
	}
	if again := resumeUpgrade(got, now, 4); !reflect.DeepEqual(again, got) {
		t.Fatalf("a second resume must not change a running phase: %+v", again.Upgrade)
	}
}

func TestIgnoreAction(t *testing.T) {
	t.Run("during an upgrade", func(t *testing.T) {
		got := ignoreAction(inPhase(v1alpha1.PhaseBackup), "resume ignored: phase Backup has not failed", 2)
		if got.Upgrade.Message != "resume ignored: phase Backup has not failed" || got.Phase != v1alpha1.PhaseBackup {
			t.Fatalf("status %+v upgrade %+v", got, got.Upgrade)
		}
	})
	t.Run("without an upgrade", func(t *testing.T) {
		got := ignoreAction(v1alpha1.ClusterStatus{Version: "v1", Phase: v1alpha1.PhaseIdle}, "abort ignored: no upgrade in progress", 2)
		progressing := conditionOf(got, v1alpha1.ConditionProgressing)
		if progressing.Status != metav1.ConditionFalse || progressing.Reason != "ActionIgnored" || progressing.Message != "abort ignored: no upgrade in progress" {
			t.Fatalf("progressing %+v", progressing)
		}
	})
}

func TestSettleKeepsTheLastReason(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC))
	upgraded := advance(inPhase(v1alpha1.PhaseVerify), v1alpha1.PhaseVerify, phaseResult{Done: true}, now, 5)
	got := settle(upgraded, 6)
	available := conditionOf(got, v1alpha1.ConditionAvailable)
	progressing := conditionOf(got, v1alpha1.ConditionProgressing)
	if got.Phase != v1alpha1.PhaseIdle || available.Reason != v1alpha1.ReasonUpgraded || progressing.Reason != v1alpha1.ReasonUpgraded || available.ObservedGeneration != 6 {
		t.Fatalf("available %+v progressing %+v", available, progressing)
	}
	fresh := settle(v1alpha1.ClusterStatus{Version: "v1"}, 1)
	if conditionOf(fresh, v1alpha1.ConditionAvailable).Reason != "Installed" || conditionOf(fresh, v1alpha1.ConditionProgressing).Reason != "Installed" {
		t.Fatalf("fresh %+v", fresh.Conditions)
	}
}

func TestMergeNodeUpgradeSpec(t *testing.T) {
	current := v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v2", From: "v1", Depot: "http://10.0.0.11:9480/v2/amd64", Attempt: 1, Steps: []string{v1alpha1.StepPreload}}
	want := v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v2", From: "v1", Depot: "http://other", Backup: "/var/lib/bedrock/backups/b.tar.gz", Attempt: 2, Steps: []string{v1alpha1.StepBackup, v1alpha1.StepPreload}}
	got := mergeNodeUpgradeSpec(current, want)
	expected := v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v2", From: "v1", Depot: "http://10.0.0.11:9480/v2/amd64", Backup: "/var/lib/bedrock/backups/b.tar.gz", Attempt: 2, Steps: []string{v1alpha1.StepPreload, v1alpha1.StepBackup}}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("merged %+v, want %+v", got, expected)
	}
	lower := mergeNodeUpgradeSpec(got, v1alpha1.NodeUpgradeSpec{Attempt: 1, Backup: "/other"})
	if lower.Attempt != 2 || lower.Backup != "/var/lib/bedrock/backups/b.tar.gz" || len(lower.Steps) != 2 {
		t.Fatalf("attempt and backup must not go back: %+v", lower)
	}
	if len(current.Steps) != 1 {
		t.Fatal("input changed")
	}
}
