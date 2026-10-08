package backup

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

const eventSource = "bedrock-backup"

func SetupWithManager(mgr ctrl.Manager) error {
	recorder := mgr.GetEventRecorderFor(eventSource)
	reconcilers := []interface{ SetupWithManager(ctrl.Manager) error }{
		&TargetReconciler{Client: mgr.GetClient(), Recorder: recorder},
		&BackupReconciler{Client: mgr.GetClient(), Now: time.Now, Recorder: recorder},
		&RestoreReconciler{Client: mgr.GetClient(), Now: time.Now},
	}
	for _, reconciler := range reconcilers {
		if err := reconciler.SetupWithManager(mgr); err != nil {
			return err
		}
	}
	return nil
}
