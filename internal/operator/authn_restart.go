package operator

import (
	"context"
	"encoding/json"
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
	var revoke, waiting []string
	holding := false
	for _, host := range hosts {
		_, annotated := host.Annotations[v1alpha1.AnnotationAuthnRestart]
		switch {
		case authnRestartHolds(host, now):
			holding = true
		case authnRestartPending(host):
			waiting = append(waiting, host.Name)
		}
		if annotated && !authnRestartHolds(host, now) {
			revoke = append(revoke, host.Name)
		}
	}
	slices.Sort(revoke)
	if holding || len(waiting) == 0 {
		return "", revoke
	}
	return slices.Min(waiting), revoke
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
	if err := r.Client.List(ctx, &hosts); err != nil {
		return 0, err
	}
	grant, revoke := authnRestartGrants(hosts.Items, now)
	for _, name := range revoke {
		if err := r.annotateAuthnRestart(ctx, name, nil); err != nil {
			return 0, err
		}
	}
	if grant != "" {
		stamp := now.UTC().Format(time.RFC3339)
		if err := r.annotateAuthnRestart(ctx, grant, &stamp); err != nil {
			return 0, err
		}
	}
	return authnRestartRequeue(hosts.Items, grant, now), nil
}

func (r *HostReconciler) annotateAuthnRestart(ctx context.Context, name string, value *string) error {
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]*string{v1alpha1.AnnotationAuthnRestart: value}}})
	if err != nil {
		return err
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}}
	patch := client.RawPatch(types.MergePatchType, body)
	return client.IgnoreNotFound(r.Client.Patch(ctx, host, patch, client.FieldOwner(v1alpha1.OperatorFieldManager)))
}
