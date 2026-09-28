package hostconfig

import (
	"context"
	"errors"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var errSkipped = errors.New("skipped")

func steps() []step {
	return []step{
		{name: "packages", run: packagesStep},
		{name: "modules", run: modulesStep},
		{name: "sysctls", run: sysctlsStep},
		{name: "kernelCmdline", run: skipStep("applied by a later release")},
		{name: "units", run: unitsStep},
		{name: "nmstate", run: skipStep("applied by the nmstate handler")},
		{name: "containerdMirrors", run: mirrorsStep},
	}
}

func Apply(ctx context.Context, deps Deps, spec v1alpha1.HostConfigSpec) []v1alpha1.StepResult {
	all := steps()
	results := make([]v1alpha1.StepResult, 0, len(all))
	for _, s := range all {
		message, err := s.run(ctx, deps, spec)
		results = append(results, result(s.name, message, err))
	}
	return results
}

func result(name, message string, err error) v1alpha1.StepResult {
	switch {
	case errors.Is(err, errSkipped):
		return v1alpha1.StepResult{Name: name, State: StateSkipped, Message: message}
	case err != nil:
		return v1alpha1.StepResult{Name: name, State: StateFailed, Message: err.Error()}
	default:
		return v1alpha1.StepResult{Name: name, State: StateApplied, Message: message}
	}
}

func Failed(steps []v1alpha1.StepResult) bool {
	for _, s := range steps {
		if s.State == StateFailed {
			return true
		}
	}
	return false
}
