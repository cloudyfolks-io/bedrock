package release

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type helmHookEvents struct {
	before string
	after  string
}

func Install(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, report func(group Group, err error)) error {
	return applyGroups(ctx, c, bundle, vars, gates, interval, groupTimeout, report, SkipHook, SkipHook, helmHookEvents{})
}

func SkipHook(context.Context, Group) error {
	return nil
}

func InstallGroups(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, report func(group Group, err error), before, after func(ctx context.Context, group Group) error) error {
	return applyGroups(ctx, c, bundle, vars, gates, interval, groupTimeout, report, before, after, helmHookEvents{before: "pre-install", after: "post-install"})
}

func UpgradeGroups(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, report func(group Group, err error), before, after func(ctx context.Context, group Group) error) error {
	return applyGroups(ctx, c, bundle, vars, gates, interval, groupTimeout, report, before, after, helmHookEvents{before: "pre-upgrade", after: "post-upgrade"})
}

func applyGroups(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, report func(group Group, err error), before, after func(ctx context.Context, group Group) error, events helmHookEvents) error {
	bundle, err := Substitute(bundle, vars)
	if err != nil {
		return err
	}
	bundle, err = SetPodMTU(bundle, vars)
	if err != nil {
		return err
	}
	bundle = SetCentralTopology(bundle, vars)
	previous, err := ReadInventory(ctx, c)
	if err != nil {
		return err
	}
	applier := Applier{Client: c}
	currentGroups := make([]Group, 0, len(bundle.Groups))
	for _, group := range bundle.Groups {
		selected := forHelmEvents(group, "", events.before, events.after)
		err := applyGroup(ctx, c, applier, gates, selected, interval, groupTimeout, before, after, events)
		report(selected, err)
		if err != nil {
			return fmt.Errorf("group %s: %w", group.Name, err)
		}
		currentGroups = append(currentGroups, selected)
	}
	current := InventoryOf(currentGroups)
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

func InstallGroup(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, name string, before, after func(ctx context.Context, group Group) error) error {
	return applyNamedGroup(ctx, c, bundle, vars, gates, interval, groupTimeout, name, before, after, helmHookEvents{})
}

func UpgradeGroup(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, name string, before, after func(ctx context.Context, group Group) error) error {
	return applyNamedGroup(ctx, c, bundle, vars, gates, interval, groupTimeout, name, before, after, helmHookEvents{before: "pre-upgrade", after: "post-upgrade"})
}

func applyNamedGroup(ctx context.Context, c client.Client, bundle Bundle, vars map[string]string, gates Gates, interval, groupTimeout time.Duration, name string, before, after func(ctx context.Context, group Group) error, events helmHookEvents) error {
	bundle, err := Substitute(bundle, vars)
	if err != nil {
		return err
	}
	bundle, err = SetPodMTU(bundle, vars)
	if err != nil {
		return err
	}
	bundle = SetCentralTopology(bundle, vars)
	for _, group := range bundle.Groups {
		if group.Name != name {
			continue
		}
		selected := forHelmEvents(group, "", events.before, events.after)
		if err := applyGroup(ctx, c, Applier{Client: c}, gates, selected, interval, groupTimeout, before, after, events); err != nil {
			return fmt.Errorf("group %s: %w", group.Name, err)
		}
		return nil
	}
	return fmt.Errorf("group %s: not in the release", name)
}

func applyGroup(ctx context.Context, c client.Client, applier Applier, gates Gates, group Group, interval, timeout time.Duration, before, after func(ctx context.Context, group Group) error, events helmHookEvents) error {
	groupCtx, cancel := groupContext(ctx, timeout)
	defer cancel()
	if err := before(groupCtx, group); err != nil {
		return err
	}
	steps := []string{""}
	if events.before != "" {
		steps = []string{events.before, "", events.after}
	}
	for _, event := range steps {
		phase := forHelmEvents(group, event)
		if err := applier.Apply(groupCtx, phase); err != nil {
			return err
		}
		if err := WaitGroup(groupCtx, c, gates, phase, interval); err != nil {
			return err
		}
	}
	return after(groupCtx, group)
}
