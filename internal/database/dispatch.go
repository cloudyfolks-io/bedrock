package database

import (
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

type BackupState func(*unstructured.Unstructured) (completed *time.Time, failed bool)

func EnginePatches(kind string, spec engine.Spec) ([]engine.Patch, error) {
	switch kind {
	case v1alpha1.DatabaseEnginePostgreSQL:
		return postgres.Patches(spec)
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.Patches(spec)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.Patches(spec)
	}
	return nil, fmt.Errorf("unknown engine %q", kind)
}

func EngineSuspendPatch(kind, name string, on bool) (engine.Patch, error) {
	switch kind {
	case v1alpha1.DatabaseEnginePostgreSQL:
		return postgres.SuspendPatch(name, on), nil
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.SuspendPatch(name, on), nil
	case v1alpha1.DatabaseEngineValkey:
		return valkey.SuspendPatch(name, on), nil
	}
	return engine.Patch{}, fmt.Errorf("unknown engine %q", kind)
}

func EngineRestartPatch(kind, name, now string) (engine.Patch, error) {
	switch kind {
	case v1alpha1.DatabaseEnginePostgreSQL:
		return postgres.RestartPatch(name, now), nil
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.RestartPatch(name, now), nil
	case v1alpha1.DatabaseEngineValkey:
		return valkey.RestartPatch(name, now), nil
	}
	return engine.Patch{}, fmt.Errorf("unknown engine %q", kind)
}

func scaleGatePatches(name, projectNamespace string, replicas int) []engine.Patch {
	body := []byte(`{"spec":{"replicas":` + strconv.Itoa(replicas) + `}}`)
	return []engine.Patch{
		{GVK: DeploymentGVK, Namespace: PlatformNamespace, Name: engine.Named(name, "gate"), Body: body},
		{GVK: DeploymentGVK, Namespace: projectNamespace, Name: engine.Named(name, "proxy"), Body: body},
	}
}

func StepPatches(kind string, spec engine.Spec, projectNamespace string, steps []Step, now string) ([]engine.Patch, error) {
	var patches []engine.Patch
	for _, step := range steps {
		stepPatches, err := patchesOfStep(kind, spec, projectNamespace, step, now)
		if err != nil {
			return nil, err
		}
		patches = append(patches, stepPatches...)
	}
	return patches, nil
}

func patchesOfStep(kind string, spec engine.Spec, projectNamespace string, step Step, now string) ([]engine.Patch, error) {
	switch step {
	case StepResize, StepGrowStorage, StepUpgrade, StepRefreshImage:
		return nil, nil
	case StepRestart:
		patch, err := EngineRestartPatch(kind, spec.Name, now)
		return []engine.Patch{patch}, err
	case StepSuspend:
		patch, err := EngineSuspendPatch(kind, spec.Name, true)
		return append([]engine.Patch{patch}, scaleGatePatches(spec.Name, projectNamespace, 0)...), err
	case StepResume:
		patch, err := EngineSuspendPatch(kind, spec.Name, false)
		return append([]engine.Patch{patch}, scaleGatePatches(spec.Name, projectNamespace, 1)...), err
	}
	return nil, fmt.Errorf("unknown step %q", step)
}

func ChangesPatchSteps(steps []Step) bool {
	for _, step := range steps {
		switch step {
		case StepResize, StepGrowStorage, StepUpgrade, StepRefreshImage:
			return true
		}
	}
	return false
}

func TargetFor(kind, name string) engine.Target {
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.Target(name)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.Target(name)
	}
	return postgres.Target(name)
}

func BackupStateOf(kind string) BackupState {
	if kind == v1alpha1.DatabaseEngineMySQL {
		return mysql.BackupState
	}
	return postgres.BackupState
}

func PreUpgradeBackup(kind string, spec engine.Spec, name string) (*unstructured.Unstructured, error) {
	switch kind {
	case v1alpha1.DatabaseEnginePostgreSQL:
		return postgres.PreUpgradeBackup(spec, name), nil
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.PreUpgradeBackup(spec, name), nil
	}
	return nil, fmt.Errorf("the engine %q has no backup", kind)
}
