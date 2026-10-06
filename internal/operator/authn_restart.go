package operator

import (
	"context"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func authnRestartPending(host v1alpha1.Host) bool {
	return host.Status.Authn != nil && host.Status.Authn.WebhookRestartPending
}

func authnRestartHolds(host v1alpha1.Host, now time.Time) bool {
	return authnRestartPending(host) && v1alpha1.AuthnRestartGrantFresh(host, now)
}

func authnRestartGrants(hosts []v1alpha1.Host, now time.Time) (string, []string) {
	var revoke, waiting, expired []string
	holding := false
	for _, host := range hosts {
		_, annotated := host.Annotations[v1alpha1.AnnotationAuthnRestart]
		switch {
		case authnRestartHolds(host, now):
			holding = true
		case authnRestartPending(host):
			waiting = append(waiting, host.Name)
			if annotated {
				expired = append(expired, host.Name)
			}
		}
		if annotated && !authnRestartHolds(host, now) {
			revoke = append(revoke, host.Name)
		}
	}
	slices.Sort(revoke)
	if holding || len(waiting) == 0 {
		return "", revoke
	}
	return nextAuthnRestart(waiting, expired), revoke
}

func nextAuthnRestart(waiting, expired []string) string {
	slices.Sort(waiting)
	if len(expired) > 0 {
		after := slices.Max(expired)
		if i := slices.IndexFunc(waiting, func(name string) bool { return name > after }); i >= 0 {
			return waiting[i]
		}
	}
	return waiting[0]
}

func authnRestartRequeue(hosts []v1alpha1.Host, grant string, now time.Time) time.Duration {
	var requeue time.Duration
	if grant != "" {
		requeue = v1alpha1.AuthnRestartGrantLifetime
	}
	for _, host := range hosts {
		granted, ok := v1alpha1.AuthnRestartGrantTime(host)
		if !ok || !authnRestartHolds(host, now) {
			continue
		}
		remaining := granted.Add(v1alpha1.AuthnRestartGrantLifetime).Sub(now)
		if requeue == 0 || remaining < requeue {
			requeue = remaining
		}
	}
	return requeue
}

func (r *HostReconciler) reconcileAuthnRestart(ctx context.Context, now time.Time) (time.Duration, error) {
	var hosts v1alpha1.HostList
	if err := r.APIReader.List(ctx, &hosts); err != nil {
		return 0, err
	}
	grant, revoke := authnRestartGrants(hosts.Items, now)
	for _, name := range revoke {
		if err := r.revokeAuthnRestart(ctx, name); err != nil {
			return 0, err
		}
	}
	if grant != "" {
		if err := r.grantAuthnRestart(ctx, grant, now); err != nil {
			return 0, err
		}
	}
	return authnRestartRequeue(hosts.Items, grant, now), nil
}

func grantAuthnRestartPatch(at time.Time) client.Patch {
	return client.RawPatch(types.MergePatchType, fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`, v1alpha1.AnnotationAuthnRestart, at.UTC().Format(time.RFC3339)))
}

func revokeAuthnRestartPatch() client.Patch {
	return client.RawPatch(types.MergePatchType, fmt.Appendf(nil, `{"metadata":{"annotations":{%q:null}}}`, v1alpha1.AnnotationAuthnRestart))
}

func (r *HostReconciler) grantAuthnRestart(ctx context.Context, name string, at time.Time) error {
	return r.patchHost(ctx, name, grantAuthnRestartPatch(at))
}

func (r *HostReconciler) revokeAuthnRestart(ctx context.Context, name string) error {
	return r.patchHost(ctx, name, revokeAuthnRestartPatch())
}

func (r *HostReconciler) patchHost(ctx context.Context, name string, patch client.Patch) error {
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return client.IgnoreNotFound(r.Client.Patch(ctx, host, patch, client.FieldOwner(v1alpha1.OperatorFieldManager)))
}
