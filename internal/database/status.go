package database

import (
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ReasonProvisioning      = "Provisioning"
	ReasonRestoring         = "Restoring"
	ReasonSuspended         = "Suspended"
	ReasonUnhealthy         = "Unhealthy"
	ReasonEngineWaiting     = "EngineNotReady"
	ReasonBootstrapping     = "BootstrapPending"
	ReasonGateWaiting       = "GateNotReady"
	ReasonProxyWaiting      = "EndpointNotReady"
	ReasonProvisionTimedOut = "ProvisionTimeout"

	backupStaleAfter = 36 * time.Hour
)

type Observation struct {
	Deleting         bool
	Suspended        bool
	Restoring        bool
	Bootstrapped     bool
	ReadyBefore      bool
	EngineReady      bool
	EngineReason     string
	GateReady        bool
	EndpointReady    bool
	Refusal          *Refusal
	Age              time.Duration
	ProvisionTimeout time.Duration
}

type Verdict struct {
	Phase   string
	Reason  string
	Message string
}

func Phase(o Observation) Verdict {
	switch {
	case o.Deleting:
		return Verdict{Phase: v1alpha1.DatabasePhaseDeleting, Reason: ReasonDeleting, Message: "the database is being deleted"}
	case o.Suspended:
		return Verdict{Phase: v1alpha1.DatabasePhaseSuspended, Reason: ReasonSuspended, Message: "the database is suspended"}
	}
	reason, message := firstProblem(o)
	if reason == "" {
		return Verdict{Phase: v1alpha1.DatabasePhaseReady, Reason: ReasonReady, Message: "the database accepts connections"}
	}
	switch {
	case o.ReadyBefore:
		return Verdict{Phase: v1alpha1.DatabasePhaseUnhealthy, Reason: reason, Message: message}
	case o.ProvisionTimeout > 0 && o.Age > o.ProvisionTimeout:
		return Verdict{Phase: v1alpha1.DatabasePhaseFailed, Reason: reason, Message: "the database was not ready after " + o.ProvisionTimeout.String() + ": " + message}
	case o.Restoring:
		return Verdict{Phase: v1alpha1.DatabasePhaseRestoring, Reason: reason, Message: message}
	}
	return Verdict{Phase: v1alpha1.DatabasePhaseProvisioning, Reason: reason, Message: message}
}

func firstProblem(o Observation) (string, string) {
	switch {
	case o.Refusal != nil:
		return o.Refusal.Reason, o.Refusal.Message
	case !o.EngineReady:
		message := o.EngineReason
		if message == "" {
			message = "the engine is not ready"
		}
		return ReasonEngineWaiting, message
	case !o.Bootstrapped:
		return ReasonBootstrapping, "the first setup of the engine has not run yet"
	case !o.GateReady:
		return ReasonGateWaiting, "the gate of the database is not ready"
	case !o.EndpointReady:
		return ReasonProxyWaiting, "the proxy in the subnet is not ready"
	}
	return "", ""
}

type BackupObservation struct {
	Enabled bool
	Age     time.Duration
	Now     time.Time
	Status  *v1alpha1.DatabaseBackupStatus
}

type BackupVerdict struct {
	Healthy bool
	Unknown bool
	Reason  string
	Message string
}

func BackupHealth(o BackupObservation) BackupVerdict {
	if !o.Enabled {
		return BackupVerdict{Healthy: true, Reason: "BackupDisabled", Message: "backups are off for this database"}
	}
	if o.Status != nil && len(o.Status.Items) > 0 && o.Status.Items[0].Phase == "failed" {
		return BackupVerdict{Reason: "BackupFailed", Message: "the newest backup failed"}
	}
	if o.Status == nil || o.Status.LastAt == nil {
		if o.Age > backupStaleAfter {
			return BackupVerdict{Reason: "BackupStale", Message: "no backup has completed yet"}
		}
		return BackupVerdict{Unknown: true, Reason: "NoBackupYet", Message: "the first backup has not completed yet"}
	}
	if o.Now.Sub(o.Status.LastAt.Time) > backupStaleAfter {
		return BackupVerdict{Reason: "BackupStale", Message: "the last completed backup is older than 36 hours"}
	}
	return BackupVerdict{Healthy: true, Reason: "BackupCurrent", Message: "a backup completed in the last 36 hours"}
}
