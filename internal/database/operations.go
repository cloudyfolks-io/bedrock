package database

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Step string

const (
	StepResize       Step = "Resize"
	StepGrowStorage  Step = "GrowStorage"
	StepUpgrade      Step = "Upgrade"
	StepRefreshImage Step = "RefreshImage"
	StepRestart      Step = "Restart"
	StepSuspend      Step = "Suspend"
	StepResume       Step = "Resume"
)

type UpgradeFacts struct {
	Targets      []string
	LatestBackup *time.Time
	PreRequested bool
	MaxAge       time.Duration
	Now          time.Time
}

type OperationFacts struct {
	PreviousClass    v1alpha1.DatabaseClassSpec
	Class            v1alpha1.DatabaseClassSpec
	ExpansionAllowed bool
	Upgrade          UpgradeFacts
}

type Result struct {
	Steps         []Step
	Refusal       *Refusal
	Hold          *Refusal
	RequestBackup bool
	RestartToken  string
}

func Plan(applied v1alpha1.DatabaseApplied, spec v1alpha1.DatabaseSpec, facts OperationFacts, annotations map[string]string) Result {
	if refusal := resizeRefusal(applied, spec, facts); refusal != nil {
		return Result{Refusal: refusal}
	}
	if refusal := upgradeRefusal(applied, spec, facts.Upgrade); refusal != nil {
		return Result{Refusal: refusal}
	}
	plan := Result{Steps: resizeSteps(applied, spec)}
	plan = withUpgrade(plan, applied, spec, facts.Upgrade)
	plan.Steps = append(plan.Steps, restartSteps(applied, annotations)...)
	plan.RestartToken = annotations[v1alpha1.AnnotationRestart]
	plan.Steps = append(plan.Steps, suspendSteps(applied, spec)...)
	return plan
}

func resizeRefusal(applied v1alpha1.DatabaseApplied, spec v1alpha1.DatabaseSpec, facts OperationFacts) *Refusal {
	classChanged := spec.Class != applied.Class
	grows := spec.StorageGi > applied.StorageGi
	if classChanged && facts.PreviousClass.HighAvailability != facts.Class.HighAvailability {
		return &Refusal{Reason: ReasonClassChangeInvalid, Message: "the new class must keep the high availability of the current class"}
	}
	if (classChanged || grows) && (spec.StorageGi < facts.Class.MinStorageGi || spec.StorageGi > facts.Class.MaxStorageGi) {
		return &Refusal{Reason: ReasonStorageOutOfRange, Message: fmt.Sprintf("storageGi must be from %d to %d for the class %s", facts.Class.MinStorageGi, facts.Class.MaxStorageGi, spec.Class)}
	}
	if grows && !facts.ExpansionAllowed {
		return &Refusal{Reason: ReasonExpansionNotSupported, Message: "the storage class does not allow volume expansion"}
	}
	return nil
}

func upgradeRefusal(applied v1alpha1.DatabaseApplied, spec v1alpha1.DatabaseSpec, facts UpgradeFacts) *Refusal {
	if spec.Version == applied.Version || slices.Contains(facts.Targets, spec.Version) {
		return nil
	}
	return &Refusal{Reason: ReasonVersionNotAllowed, Message: "version " + spec.Version + " is not an enabled upgrade target of version " + applied.Version}
}

func resizeSteps(applied v1alpha1.DatabaseApplied, spec v1alpha1.DatabaseSpec) []Step {
	var steps []Step
	if spec.Class != applied.Class {
		steps = append(steps, StepResize)
	}
	if spec.StorageGi > applied.StorageGi {
		steps = append(steps, StepGrowStorage)
	}
	return steps
}

func backupIsFresh(facts UpgradeFacts) bool {
	return facts.LatestBackup != nil && facts.Now.Sub(*facts.LatestBackup) <= facts.MaxAge
}

func withUpgrade(plan Result, applied v1alpha1.DatabaseApplied, spec v1alpha1.DatabaseSpec, facts UpgradeFacts) Result {
	switch {
	case spec.Version == applied.Version:
		return plan
	case spec.Engine == v1alpha1.DatabaseEngineValkey, backupIsFresh(facts):
		plan.Steps = append(plan.Steps, StepUpgrade)
	default:
		plan.Hold = &Refusal{Reason: ReasonBackupRequired, Message: "a backup younger than " + facts.MaxAge.String() + " must exist before the upgrade"}
		plan.RequestBackup = !facts.PreRequested
	}
	return plan
}

func restartSteps(applied v1alpha1.DatabaseApplied, annotations map[string]string) []Step {
	token := annotations[v1alpha1.AnnotationRestart]
	if token == "" || token == applied.RestartToken {
		return nil
	}
	if refresh, _ := strconv.ParseBool(annotations[v1alpha1.AnnotationRefreshImage]); refresh {
		return []Step{StepRefreshImage, StepRestart}
	}
	return []Step{StepRestart}
}

func suspendSteps(applied v1alpha1.DatabaseApplied, spec v1alpha1.DatabaseSpec) []Step {
	switch {
	case spec.Suspended && !applied.Suspended:
		return []Step{StepSuspend}
	case !spec.Suspended && applied.Suspended:
		return []Step{StepResume}
	}
	return nil
}

func ImageToApply(statusImage, appliedVersion, desiredVersion, catalogImage string, refresh bool) string {
	if statusImage == "" || refresh || desiredVersion != appliedVersion {
		return catalogImage
	}
	return statusImage
}

func PreUpgradeBackupName(uid string, now time.Time) string {
	return uid + "-pre-" + strconv.FormatInt(now.Unix(), 10)
}
