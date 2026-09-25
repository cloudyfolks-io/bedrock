package agent

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/hostconfig"
	"github.com/cloudyfolks-labs/bedrock/internal/maintenance"
	"github.com/cloudyfolks-labs/bedrock/internal/pkgmgr"
)

type Deps struct {
	Exec      host.Exec
	Root      string
	Node      string
	Now       func() time.Time
	Interval  time.Duration
	Inventory func(context.Context, host.Exec, string) (v1alpha1.Inventory, error)
	Apply     func(context.Context, hostconfig.Deps, v1alpha1.HostConfigSpec) []v1alpha1.StepResult
	Packages  pkgmgr.Manager
	Version   string
	Hostname  func() (string, error)
	FreeBytes func(string) (uint64, error)
}

func Run(ctx context.Context, c client.WithWatch, deps Deps) error {
	ticker := time.NewTicker(deps.Interval)
	defer ticker.Stop()
	events, stop := ensureWatch(ctx, c, deps.Node)
	defer func() { stop() }()
	for {
		if err := Tick(ctx, c, deps); err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		}
		for ready := false; !ready; {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if events == nil {
					events, stop = ensureWatch(ctx, c, deps.Node)
				}
				ready = true
			case _, open := <-events:
				if open {
					ready = true
				} else {
					stop()
					events, stop = nil, func() {}
				}
			}
		}
	}
}

func ensureWatch(ctx context.Context, c client.WithWatch, node string) (<-chan struct{}, func()) {
	events, stop, err := watchOwn(ctx, c, node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return nil, func() {}
	}
	return events, stop
}

func watchOwn(ctx context.Context, c client.WithWatch, name string) (<-chan struct{}, func(), error) {
	selector := client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("metadata.name", name)}
	hosts, err := c.Watch(ctx, &v1alpha1.HostList{}, selector)
	if err != nil {
		return nil, nil, err
	}
	configs, err := c.Watch(ctx, &v1alpha1.HostConfigList{}, selector)
	if err != nil {
		hosts.Stop()
		return nil, nil, err
	}
	events := make(chan struct{}, 1)
	var forwarding sync.WaitGroup
	forwarding.Add(2)
	forward := func(in <-chan watch.Event) {
		defer forwarding.Done()
		for range in {
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}
	go forward(hosts.ResultChan())
	go forward(configs.ResultChan())
	go func() {
		forwarding.Wait()
		close(events)
	}()
	return events, func() { hosts.Stop(); configs.Stop() }, nil
}

func Tick(ctx context.Context, c client.Client, deps Deps) error {
	var current v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: deps.Node}, &current); err != nil {
		return client.IgnoreNotFound(err)
	}
	inv, invErr := deps.Inventory(ctx, deps.Exec, deps.Root)
	next := v1alpha1.HostStatus{
		Inventory:    inv,
		Applied:      current.Status.Applied,
		AgentVersion: deps.Version,
		K0sVersion:   k0sVersion(ctx, deps.Exec),
		Hostname:     hostname(deps),
		Checks:       hostChecks(ctx, deps),
		Restore:      current.Status.Restore,
	}
	if invErr != nil {
		next.Inventory = current.Status.Inventory
	}
	var conditions []metav1.Condition
	if current.Spec.Management.Enabled {
		applied, managed := manage(ctx, c, deps, current)
		next.Applied = applied
		conditions = append(conditions, managed, maintain(ctx, deps, current))
	} else {
		conditions = append(conditions, condition(v1alpha1.ConditionManagementApplied, metav1.ConditionFalse, "ManagementDisabled", "", current.Generation))
	}
	next.Conditions = stable(current.Status.Conditions, conditions)
	if err := ApplyStatus(ctx, c, deps.Node, next); err != nil {
		return err
	}
	return invErr
}

func manage(ctx context.Context, c client.Client, deps Deps, current v1alpha1.Host) (*v1alpha1.AppliedConfig, metav1.Condition) {
	var config v1alpha1.HostConfig
	err := c.Get(ctx, client.ObjectKey{Name: deps.Node}, &config)
	if errors.IsNotFound(err) {
		return current.Status.Applied, condition(v1alpha1.ConditionManagementApplied, metav1.ConditionFalse, "NoHostConfig", "no HostConfig for this host yet", current.Generation)
	}
	if err != nil {
		return current.Status.Applied, condition(v1alpha1.ConditionManagementApplied, metav1.ConditionFalse, "ReadFailed", err.Error(), current.Generation)
	}
	applied := current.Status.Applied
	if applied == nil || applied.Generation != config.Generation || hostconfig.Failed(applied.Steps) {
		steps := deps.Apply(ctx, hostconfig.Deps{Exec: deps.Exec, Root: deps.Root, Packages: deps.Packages}, config.Spec)
		applied = &v1alpha1.AppliedConfig{Generation: config.Generation, Steps: steps}
	}
	if hostconfig.Failed(applied.Steps) {
		return applied, condition(v1alpha1.ConditionManagementApplied, metav1.ConditionFalse, "StepFailed", firstFailure(applied.Steps), current.Generation)
	}
	return applied, condition(v1alpha1.ConditionManagementApplied, metav1.ConditionTrue, "Applied", "", current.Generation)
}

func maintain(ctx context.Context, deps Deps, current v1alpha1.Host) metav1.Condition {
	if current.Spec.MaintenanceWindow == "" {
		return condition(v1alpha1.ConditionRebootPending, metav1.ConditionFalse, "NoWindow", "", current.Generation)
	}
	window, err := maintenance.Parse(current.Spec.MaintenanceWindow)
	if err != nil {
		return condition(v1alpha1.ConditionRebootPending, metav1.ConditionFalse, "InvalidWindow", err.Error(), current.Generation)
	}
	if !window.Open(deps.Now()) {
		return keepOrFalse(current, v1alpha1.ConditionRebootPending, "WindowClosed")
	}
	if err := deps.Packages.SecurityUpdate(ctx); err != nil {
		return condition(v1alpha1.ConditionRebootPending, metav1.ConditionFalse, "UpdateFailed", err.Error(), current.Generation)
	}
	reboot, err := deps.Packages.RebootRequired(ctx)
	if err != nil {
		return condition(v1alpha1.ConditionRebootPending, metav1.ConditionFalse, "ProbeFailed", err.Error(), current.Generation)
	}
	if !reboot {
		return condition(v1alpha1.ConditionRebootPending, metav1.ConditionFalse, "None", "", current.Generation)
	}
	if err := pkgmgr.TouchSentinel(deps.Root); err != nil {
		return condition(v1alpha1.ConditionRebootPending, metav1.ConditionTrue, "SentinelFailed", err.Error(), current.Generation)
	}
	return condition(v1alpha1.ConditionRebootPending, metav1.ConditionTrue, "SecurityUpdate", "reboot handed to kured", current.Generation)
}

func keepOrFalse(current v1alpha1.Host, conditionType, reason string) metav1.Condition {
	for _, c := range current.Status.Conditions {
		if c.Type == conditionType && c.Status == metav1.ConditionTrue {
			return c
		}
	}
	return condition(conditionType, metav1.ConditionFalse, reason, "", current.Generation)
}

func stable(existing, next []metav1.Condition) []metav1.Condition {
	settled := make([]metav1.Condition, 0, len(next))
	for _, cond := range next {
		settled = append(settled, reuseTransition(existing, cond))
	}
	return settled
}

func reuseTransition(existing []metav1.Condition, cond metav1.Condition) metav1.Condition {
	for _, old := range existing {
		if old.Type == cond.Type && old.Status == cond.Status && old.Reason == cond.Reason {
			cond.LastTransitionTime = old.LastTransitionTime
			return cond
		}
	}
	return cond
}

func firstFailure(steps []v1alpha1.StepResult) string {
	for _, s := range steps {
		if s.State == hostconfig.StateFailed {
			return s.Name + ": " + s.Message
		}
	}
	return ""
}

func condition(conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) metav1.Condition {
	return metav1.Condition{Type: conditionType, Status: status, Reason: reason, Message: message, ObservedGeneration: generation, LastTransitionTime: metav1.Now()}
}
