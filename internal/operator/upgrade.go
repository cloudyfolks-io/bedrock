package operator

import (
	"context"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

const (
	routeInstall = "install"
	routeSteady  = "steady"
	routeAwait   = "await"
	routeOld     = "old"
	routeNew     = "new"

	decisionStart       = "start"
	decisionRun         = "run"
	decisionWait        = "wait"
	decisionResume      = "resume"
	decisionIgnore      = "ignore"
	decisionRefuseAbort = "refuseAbort"
	decisionRetarget    = "retarget"

	reasonInstalled     = "Installed"
	reasonActionIgnored = "ActionIgnored"
	reasonPassed        = "Passed"
)

type upgradeEnv struct {
	Client   client.Client
	Bundle   release.Bundle
	Interval time.Duration
	Exec     execFunc
}

type phaseResult struct {
	Done    bool
	Message string
	Failure string
	Blocked string
}

type phaseFunc func(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error)

type decision struct {
	Kind    string
	Message string
}

func upgradePhases() []string {
	return []string{v1alpha1.PhasePreflight, v1alpha1.PhaseBackup, v1alpha1.PhasePreload, v1alpha1.PhaseControlPlane, v1alpha1.PhaseComponents, v1alpha1.PhaseWorkers, v1alpha1.PhaseVerify}
}

func noRollbackPhases() []string {
	return []string{v1alpha1.PhaseComponents, v1alpha1.PhaseWorkers, v1alpha1.PhaseVerify}
}

func route(cluster v1alpha1.Cluster, embedded string) string {
	running := cluster.Status.Version
	target := upgradeTarget(cluster)
	switch {
	case running == "" && cluster.Spec.DesiredVersion == embedded:
		return routeInstall
	case running == "":
		return routeAwait
	case target == running && embedded == running:
		return routeSteady
	case target == running:
		return routeAwait
	case embedded == running:
		return routeOld
	case embedded == target:
		return routeNew
	}
	return routeAwait
}

func upgradeTarget(cluster v1alpha1.Cluster) string {
	if cluster.Status.Upgrade != nil {
		return cluster.Status.Upgrade.To
	}
	return cluster.Spec.DesiredVersion
}

func nextPhase(phase string) string {
	phases := upgradePhases()
	index := slices.Index(phases, phase)
	if index < 0 || index == len(phases)-1 {
		return v1alpha1.PhaseIdle
	}
	return phases[index+1]
}

func activePhase(status v1alpha1.ClusterStatus) string {
	if status.Phase == v1alpha1.PhaseFailed && status.Upgrade != nil {
		return status.Upgrade.FailedPhase
	}
	return status.Phase
}

func decide(cluster v1alpha1.Cluster) decision {
	status := cluster.Status
	action := cluster.Spec.Upgrade.Action
	phase := activePhase(status)
	switch {
	case action == v1alpha1.UpgradeActionResume && (status.Phase != v1alpha1.PhaseFailed || status.Upgrade == nil):
		return decision{Kind: decisionIgnore, Message: fmt.Sprintf("resume ignored: phase %s has not failed", status.Phase)}
	case action == v1alpha1.UpgradeActionResume:
		return decision{Kind: decisionResume}
	case action == v1alpha1.UpgradeActionAbort && slices.Contains(noRollbackPhases(), phase):
		return decision{Kind: decisionRefuseAbort, Message: fmt.Sprintf("abort refused in %s: no rollback after Components started; resume continues the upgrade", phase)}
	case status.Upgrade == nil:
		return decision{Kind: decisionStart}
	case status.Upgrade.To != cluster.Spec.DesiredVersion:
		return decision{Kind: decisionRetarget, Message: fmt.Sprintf("desiredVersion is %s while the upgrade to %s runs: set it back to %s, or abort", cluster.Spec.DesiredVersion, status.Upgrade.To, status.Upgrade.To)}
	case status.Phase == v1alpha1.PhaseFailed:
		return decision{Kind: decisionWait}
	}
	return decision{Kind: decisionRun}
}

func startUpgrade(status v1alpha1.ClusterStatus, to string, now metav1.Time, generation int64) v1alpha1.ClusterStatus {
	if status.Upgrade != nil {
		return status
	}
	next := *status.DeepCopy()
	next.Phase = v1alpha1.PhasePreflight
	next.Upgrade = &v1alpha1.UpgradeStatus{From: status.Version, To: to, StartedAt: now, PhaseStartedAt: now, Attempt: 1}
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionTrue, v1alpha1.ReasonUpgrading, "", generation)
	return next
}

func advance(status v1alpha1.ClusterStatus, ran string, result phaseResult, now metav1.Time, generation int64) v1alpha1.ClusterStatus {
	switch {
	case status.Upgrade == nil || status.Phase != ran:
		return status
	case result.Blocked != "":
		return blockUpgrade(status, result.Blocked, generation)
	case result.Failure != "":
		return failPhase(status, result.Failure, generation)
	case result.Done && ran == v1alpha1.PhaseVerify:
		return finishUpgrade(status, generation)
	case result.Done && ran == v1alpha1.PhasePreflight:
		return passPreflight(enterPhase(status, nextPhase(ran), now), generation)
	case result.Done:
		return enterPhase(status, nextPhase(ran), now)
	}
	return withMessage(status, result.Message)
}

func enterPhase(status v1alpha1.ClusterStatus, phase string, now metav1.Time) v1alpha1.ClusterStatus {
	next := *status.DeepCopy()
	next.Phase = phase
	next.Upgrade.PhaseStartedAt = now
	next.Upgrade.Message = ""
	return next
}

func passPreflight(status v1alpha1.ClusterStatus, generation int64) v1alpha1.ClusterStatus {
	next := *status.DeepCopy()
	setCondition(&next, v1alpha1.ConditionUpgradeBlocked, metav1.ConditionFalse, reasonPassed, "", generation)
	return next
}

func withMessage(status v1alpha1.ClusterStatus, message string) v1alpha1.ClusterStatus {
	if status.Upgrade == nil {
		return status
	}
	next := *status.DeepCopy()
	next.Upgrade.Message = message
	return next
}

func failPhase(status v1alpha1.ClusterStatus, message string, generation int64) v1alpha1.ClusterStatus {
	if status.Upgrade == nil {
		return status
	}
	next := *status.DeepCopy()
	next.Upgrade.FailedPhase = activePhase(status)
	next.Upgrade.Message = message
	next.Phase = v1alpha1.PhaseFailed
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonPhaseFailed, message, generation)
	return next
}

func blockUpgrade(status v1alpha1.ClusterStatus, message string, generation int64) v1alpha1.ClusterStatus {
	next := *status.DeepCopy()
	next.Phase = v1alpha1.PhaseIdle
	next.Upgrade = nil
	setCondition(&next, v1alpha1.ConditionUpgradeBlocked, metav1.ConditionTrue, v1alpha1.ReasonBlocked, message, generation)
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonBlocked, message, generation)
	return next
}

func finishUpgrade(status v1alpha1.ClusterStatus, generation int64) v1alpha1.ClusterStatus {
	next := *status.DeepCopy()
	next.Version = status.Upgrade.To
	next.Upgrade = nil
	next.Phase = v1alpha1.PhaseIdle
	setCondition(&next, v1alpha1.ConditionAvailable, metav1.ConditionTrue, v1alpha1.ReasonUpgraded, "", generation)
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonUpgraded, "", generation)
	return next
}

func resumeUpgrade(status v1alpha1.ClusterStatus, now metav1.Time, generation int64) v1alpha1.ClusterStatus {
	if status.Upgrade == nil || status.Phase != v1alpha1.PhaseFailed {
		return status
	}
	next := *status.DeepCopy()
	next.Phase = status.Upgrade.FailedPhase
	next.Upgrade.Attempt = status.Upgrade.Attempt + 1
	next.Upgrade.FailedPhase = ""
	next.Upgrade.PhaseStartedAt = now
	next.Upgrade.Message = ""
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionTrue, v1alpha1.ReasonUpgrading, "", generation)
	return next
}

func ignoreAction(status v1alpha1.ClusterStatus, message string, generation int64) v1alpha1.ClusterStatus {
	if status.Upgrade != nil {
		return withMessage(status, message)
	}
	next := *status.DeepCopy()
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, reasonActionIgnored, message, generation)
	return next
}

func settle(status v1alpha1.ClusterStatus, generation int64) v1alpha1.ClusterStatus {
	next := *status.DeepCopy()
	next.Phase = v1alpha1.PhaseIdle
	available := lastCondition(status.Conditions, v1alpha1.ConditionAvailable, metav1.ConditionTrue)
	progressing := lastCondition(status.Conditions, v1alpha1.ConditionProgressing, metav1.ConditionFalse)
	setCondition(&next, v1alpha1.ConditionAvailable, metav1.ConditionTrue, available.Reason, available.Message, generation)
	setCondition(&next, v1alpha1.ConditionProgressing, metav1.ConditionFalse, progressing.Reason, progressing.Message, generation)
	return next
}

func lastCondition(conditions []metav1.Condition, conditionType string, value metav1.ConditionStatus) metav1.Condition {
	current := meta.FindStatusCondition(conditions, conditionType)
	if current == nil || current.Status != value {
		return metav1.Condition{Type: conditionType, Status: value, Reason: reasonInstalled}
	}
	return *current
}

func runUpgrade(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster, phases map[string]phaseFunc) (ctrl.Result, error) {
	now := metav1.Now()
	generation := cluster.Generation
	again := ctrl.Result{RequeueAfter: env.Interval}
	switch d := decide(cluster); d.Kind {
	case decisionStart:
		return again, writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
			*s = startUpgrade(*s, cluster.Spec.DesiredVersion, now, generation)
		})
	case decisionIgnore:
		return again, answerAction(ctx, env.Client, cluster, func(s v1alpha1.ClusterStatus) v1alpha1.ClusterStatus {
			return ignoreAction(s, d.Message, generation)
		})
	case decisionRefuseAbort:
		return again, answerAction(ctx, env.Client, cluster, func(s v1alpha1.ClusterStatus) v1alpha1.ClusterStatus {
			return failPhase(s, d.Message, generation)
		})
	case decisionResume:
		if err := raiseAttempts(ctx, env.Client, cluster.Status.Upgrade.To, cluster.Status.Upgrade.Attempt+1); err != nil {
			return ctrl.Result{}, err
		}
		return again, answerAction(ctx, env.Client, cluster, func(s v1alpha1.ClusterStatus) v1alpha1.ClusterStatus {
			return resumeUpgrade(s, now, generation)
		})
	case decisionRetarget:
		return again, writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
			*s = withMessage(*s, d.Message)
		})
	case decisionWait:
		return ctrl.Result{}, nil
	}
	run, ok := phases[cluster.Status.Phase]
	if !ok {
		return again, nil
	}
	result, err := run(ctx, env, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	return again, writeClusterStatus(ctx, env.Client, func(s *v1alpha1.ClusterStatus) {
		*s = advance(*s, cluster.Status.Phase, result, now, generation)
	})
}

func answerAction(ctx context.Context, c client.Client, cluster v1alpha1.Cluster, transform func(v1alpha1.ClusterStatus) v1alpha1.ClusterStatus) error {
	if err := writeClusterStatus(ctx, c, func(s *v1alpha1.ClusterStatus) {
		*s = transform(*s)
	}); err != nil {
		return err
	}
	return clearAction(ctx, c, cluster)
}

func oldPhases() map[string]phaseFunc {
	return map[string]phaseFunc{
		v1alpha1.PhasePreflight: preflight,
		v1alpha1.PhaseBackup:    backup,
		v1alpha1.PhasePreload:   preloadThenSwitch,
	}
}

func newPhases() map[string]phaseFunc {
	return map[string]phaseFunc{
		v1alpha1.PhasePreflight: preflight,
		v1alpha1.PhaseBackup:    backup,
		v1alpha1.PhasePreload:   preload,
	}
}
