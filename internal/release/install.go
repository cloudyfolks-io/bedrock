package release

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Install(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, report func(group Group, err error)) error {
	return InstallGroups(ctx, c, bundle, vars, gates, interval, groupTimeout, report, SkipHook, SkipHook)
}

func SkipHook(context.Context, Group) error {
	return nil
}

func InstallGroups(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, report func(group Group, err error), before, after func(ctx context.Context, group Group) error) error {
	bundle, err := Substitute(bundle, vars)
	if err != nil {
		return err
	}
	previous, err := ReadInventory(ctx, c)
	if err != nil {
		return err
	}
	applier := Applier{Client: c}
	for _, group := range bundle.Groups {
		groupCtx, cancel := groupContext(ctx, groupTimeout)
		err := before(groupCtx, group)
		if err == nil {
			err = applier.Apply(groupCtx, group)
		}
		if err == nil {
			err = WaitGroup(groupCtx, c, gates, group, interval)
		}
		if err == nil {
			err = after(groupCtx, group)
		}
		cancel()
		report(group, err)
		if err != nil {
			return fmt.Errorf("group %s: %w", group.Name, err)
		}
	}
	current := InventoryOf(bundle.Groups)
	if err := applier.Prune(ctx, previous, current); err != nil {
		return err
	}
	return WriteInventory(ctx, c, current)
}

func groupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
