package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
	"github.com/cloudyfolks-io/bedrock/internal/registry/harbor"
)

const (
	conditionReady     = "Ready"
	reasonReady        = "Ready"
	reasonNotAvailable = "RegistryNotAvailable"
	reasonNameTaken    = "NameTaken"
	reasonNotEmpty     = "NotEmpty"
	reasonPurge        = "PurgeIncomplete"
	reasonHarbor       = "HarborError"
	reasonWebhook      = "WebhookSecretMissing"
	reasonSettings     = "InvalidSettings"
	usageInterval      = 5 * time.Minute
	unavailableRequeue = 15 * time.Second
	terminatingRequeue = 30 * time.Second
	purgePageSize      = 100
	maxPurgePages      = 100
)

type RegistryReconciler struct {
	Client     client.Client
	BaseURL    string
	HTTP       *http.Client
	PurgePages int
}

type facts struct {
	projectName string
	projectID   int64
	summary     *harbor.Summary
	host        string
}

func (r *RegistryReconciler) connector() Connector {
	return Connector{Client: r.Client, BaseURL: r.BaseURL, HTTP: r.HTTP}
}

func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var reg v1alpha1.Registry
	if err := r.Client.Get(ctx, req.NamespacedName, &reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !reg.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &reg)
	}
	if err := r.addFinalizer(ctx, &reg); err != nil {
		return ctrl.Result{}, err
	}
	got, out, err := r.reconcileProject(ctx, &reg)
	if writeErr := r.writeStatus(ctx, &reg, got, out); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: out.requeue}, nil
}

func (r *RegistryReconciler) reconcileProject(ctx context.Context, reg *v1alpha1.Registry) (facts, result, error) {
	rules, err := LoadRules(ctx, r.Client)
	if err != nil {
		return facts{}, result{reason: reasonSettings, message: err.Error(), requeue: usageInterval}, nil
	}
	got := facts{host: rules.Host}
	name := ProjectName(reg)
	if reason, message := nameProblem(name, rules, approved(reg)); reason != "" {
		return got, result{reason: reason, message: message, requeue: usageInterval}, nil
	}
	harborClient, unavailable, err := r.connector().Harbor(ctx)
	if err != nil {
		return got, result{}, err
	}
	if unavailable != "" {
		return got, result{reason: reasonNotAvailable, message: unavailable, requeue: unavailableRequeue}, nil
	}
	got, out, err := r.converge(ctx, reg, harborClient, rules, name, got)
	return got, harborOutcome(out, err), harborError(err)
}

func harborOutcome(out result, err error) result {
	switch {
	case err == nil:
		return out
	case errors.Is(err, harbor.ErrUnavailable):
		return result{reason: reasonNotAvailable, message: err.Error(), requeue: unavailableRequeue}
	}
	return result{reason: reasonHarbor, message: err.Error(), requeue: unavailableRequeue}
}

func harborError(err error) error {
	if err == nil || errors.Is(err, harbor.ErrUnavailable) {
		return nil
	}
	return err
}

func (r *RegistryReconciler) converge(ctx context.Context, reg *v1alpha1.Registry, hc *harbor.Client, rules Rules, name string, got facts) (facts, result, error) {
	project, taken, err := r.ensureProject(ctx, reg, hc, rules, name)
	if err != nil {
		return got, result{}, err
	}
	if taken {
		return got, result{reason: reasonNameTaken, message: "the Harbor project " + name + " belongs to someone else; set the annotation " + AnnotationAdopt + " (administrators only) to adopt it", requeue: usageInterval}, nil
	}
	got.projectName, got.projectID = name, project.ID
	if summary, err := hc.ProjectSummary(ctx, name); err == nil {
		got.summary = &summary
	}
	if err := r.applyQuota(ctx, hc, name, quotaGB(reg, rules), got.summary); err != nil {
		return got, result{}, err
	}
	if got.summary != nil {
		got.summary.HardBytes = harbor.QuotaBytes(quotaGB(reg, rules))
	}
	if err := applyRetention(ctx, hc, name, reg.Spec.Retention); err != nil {
		return got, result{}, err
	}
	if out, err := r.applyWebhook(ctx, reg, hc, name); err != nil || out.reason != "" {
		return got, out, err
	}
	return got, result{ready: true, reason: reasonReady, message: "the Harbor project " + name + " is ready", requeue: usageInterval}, nil
}

func (r *RegistryReconciler) ensureProject(ctx context.Context, reg *v1alpha1.Registry, hc *harbor.Client, rules Rules, name string) (harbor.Project, bool, error) {
	owned := reg.Status.ProjectName == name
	project, err := hc.GetProject(ctx, name)
	switch {
	case err == nil && owned:
		return project, false, nil
	case err == nil && adopting(reg):
		return project, false, r.recordProject(ctx, reg, name)
	case err == nil:
		return harbor.Project{}, true, nil
	case !errors.Is(err, harbor.ErrNotFound):
		return harbor.Project{}, false, err
	}
	if !owned {
		if err := r.recordProject(ctx, reg, name); err != nil {
			return harbor.Project{}, false, err
		}
	}
	err = hc.CreateProject(ctx, name, quotaGB(reg, rules))
	if errors.Is(err, harbor.ErrConflict) && !owned {
		return harbor.Project{}, true, r.recordProject(ctx, reg, "")
	}
	if err != nil && !errors.Is(err, harbor.ErrConflict) {
		return harbor.Project{}, false, err
	}
	project, err = hc.GetProject(ctx, name)
	return project, false, err
}

func (r *RegistryReconciler) recordProject(ctx context.Context, reg *v1alpha1.Registry, name string) error {
	if err := r.mutateStatus(ctx, reg, func(status *v1alpha1.RegistryStatus) { status.ProjectName = name }); err != nil {
		return err
	}
	reg.Status.ProjectName = name
	return nil
}

func (r *RegistryReconciler) applyQuota(ctx context.Context, hc *harbor.Client, name string, gb int64, summary *harbor.Summary) error {
	if summary == nil || summary.HardBytes == harbor.QuotaBytes(gb) {
		return nil
	}
	return hc.SetQuota(ctx, name, gb)
}

func applyRetention(ctx context.Context, hc *harbor.Client, name string, spec *v1alpha1.RegistryRetention) error {
	if !hasRetention(spec) {
		existing, err := hc.GetRetention(ctx, name)
		if err != nil || existing == nil || len(existing.Rules) == 0 {
			return err
		}
	}
	retention, err := harbor.BuildRetention(retentionInput(spec))
	if err != nil {
		return err
	}
	return hc.SetRetention(ctx, name, retention)
}

func (r *RegistryReconciler) applyWebhook(ctx context.Context, reg *v1alpha1.Registry, hc *harbor.Client, name string) (result, error) {
	webhook := reg.Spec.Webhook
	if webhook == nil {
		return result{}, hc.DeleteWebhook(ctx, name)
	}
	header, found, err := r.webhookHeader(ctx, reg.Namespace, webhook.AuthHeaderSecretRef)
	if err != nil {
		return result{}, err
	}
	if !found {
		return result{reason: reasonWebhook, message: "the Secret " + webhook.AuthHeaderSecretRef.Name + " has no key " + webhook.AuthHeaderSecretRef.Key, requeue: unavailableRequeue}, nil
	}
	return result{}, hc.EnsureWebhook(ctx, name, webhook.URL, header)
}

func (r *RegistryReconciler) webhookHeader(ctx context.Context, namespace string, ref *v1alpha1.RegistrySecretKeyRef) (string, bool, error) {
	if ref == nil {
		return "", true, nil
	}
	var secret corev1.Secret
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, &secret)
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	value, found := secret.Data[ref.Key]
	return string(value), found, nil
}

func (r *RegistryReconciler) finalize(ctx context.Context, reg *v1alpha1.Registry) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(reg, platform.Finalizer("Registry")) {
		return ctrl.Result{}, nil
	}
	name := ProjectName(reg)
	if reg.Status.ProjectName != name {
		return ctrl.Result{}, r.removeFinalizer(ctx, reg)
	}
	hc, unavailable, err := r.connector().Harbor(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if unavailable != "" {
		return r.holdDelete(ctx, reg, result{reason: reasonNotAvailable, message: unavailable, requeue: unavailableRequeue})
	}
	out, done, err := r.removeProject(ctx, reg, hc, name)
	if err != nil {
		return r.holdDelete(ctx, reg, harborOutcome(result{}, err))
	}
	if !done {
		return r.holdDelete(ctx, reg, out)
	}
	return ctrl.Result{}, r.removeFinalizer(ctx, reg)
}

func (r *RegistryReconciler) holdDelete(ctx context.Context, reg *v1alpha1.Registry, out result) (ctrl.Result, error) {
	if err := r.writeStatus(ctx, reg, facts{projectName: reg.Status.ProjectName, projectID: reg.Status.ProjectID}, out); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: out.requeue}, nil
}

func (r *RegistryReconciler) removeProject(ctx context.Context, reg *v1alpha1.Registry, hc *harbor.Client, name string) (result, bool, error) {
	_, total, err := hc.ListRepositories(ctx, name, 1, purgePageSize)
	if errors.Is(err, harbor.ErrNotFound) {
		return result{}, true, nil
	}
	if err != nil {
		return result{}, false, err
	}
	if total > 0 && !r.forced(ctx, reg) {
		if err := r.mutateStatus(ctx, reg, func(status *v1alpha1.RegistryStatus) { status.RepositoryCount = total }); err != nil {
			return result{}, false, err
		}
		return result{reason: reasonNotEmpty, message: fmt.Sprintf("the project %s holds %d repositories; set forceDestroy to delete them", name, total), requeue: terminatingRequeue}, false, nil
	}
	if err := deleteRobots(ctx, hc, name); err != nil {
		return result{}, false, err
	}
	complete, err := purgeRepositories(ctx, hc, name, r.purgePages())
	if err != nil {
		return result{}, false, err
	}
	if !complete {
		return result{reason: reasonPurge, message: "the project " + name + " still holds repositories after one purge round; the next round continues", requeue: unavailableRequeue}, false, nil
	}
	return result{}, true, hc.DeleteProject(ctx, name)
}

func (r *RegistryReconciler) forced(ctx context.Context, reg *v1alpha1.Registry) bool {
	if reg.Spec.ForceDestroy {
		return true
	}
	ns := &metav1.PartialObjectMetadata{}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
	if err := r.Client.Get(ctx, client.ObjectKey{Name: reg.Namespace}, ns); err != nil {
		return apierrors.IsNotFound(err)
	}
	return !ns.DeletionTimestamp.IsZero()
}

func deleteRobots(ctx context.Context, hc *harbor.Client, name string) error {
	robots, err := hc.ListRobots(ctx, name)
	if err != nil {
		return err
	}
	for _, robot := range robots {
		if err := hc.DeleteRobot(ctx, robot.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *RegistryReconciler) purgePages() int {
	if r.PurgePages > 0 {
		return r.PurgePages
	}
	return maxPurgePages
}

func purgeRepositories(ctx context.Context, hc *harbor.Client, name string, pages int) (bool, error) {
	for range pages {
		repositories, _, err := hc.ListRepositories(ctx, name, 1, purgePageSize)
		if err != nil {
			return false, err
		}
		if len(repositories) == 0 {
			return true, nil
		}
		for _, repository := range repositories {
			if err := hc.DeleteRepository(ctx, name, repositoryPath(name, repository.Name)); err != nil {
				return false, err
			}
		}
	}
	remaining, _, err := hc.ListRepositories(ctx, name, 1, purgePageSize)
	return len(remaining) == 0, err
}

func repositoryPath(project, repository string) string {
	prefix := project + "/"
	if len(repository) > len(prefix) && repository[:len(prefix)] == prefix {
		return repository[len(prefix):]
	}
	return repository
}

func (r *RegistryReconciler) addFinalizer(ctx context.Context, reg *v1alpha1.Registry) error {
	return r.updateFinalizers(ctx, reg, controllerutil.AddFinalizer)
}

func (r *RegistryReconciler) removeFinalizer(ctx context.Context, reg *v1alpha1.Registry) error {
	return r.updateFinalizers(ctx, reg, controllerutil.RemoveFinalizer)
}

func (r *RegistryReconciler) updateFinalizers(ctx context.Context, reg *v1alpha1.Registry, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.Registry
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(reg), &latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !change(&latest, platform.Finalizer("Registry")) {
			return nil
		}
		if err := r.Client.Update(ctx, &latest, client.FieldOwner(v1alpha1.RegistryFieldManager)); err != nil {
			return err
		}
		reg.ResourceVersion = latest.ResourceVersion
		reg.Finalizers = latest.Finalizers
		return nil
	})
}

func (r *RegistryReconciler) mutateStatus(ctx context.Context, reg *v1alpha1.Registry, mutate func(*v1alpha1.RegistryStatus)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.Registry
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(reg), &latest); err != nil {
			return err
		}
		mutate(&latest.Status)
		return r.Client.Status().Update(ctx, &latest, client.FieldOwner(v1alpha1.RegistryFieldManager))
	})
}

func (r *RegistryReconciler) writeStatus(ctx context.Context, reg *v1alpha1.Registry, got facts, out result) error {
	status := metav1.ConditionFalse
	if out.ready {
		status = metav1.ConditionTrue
	}
	return r.mutateStatus(ctx, reg, func(current *v1alpha1.RegistryStatus) {
		current.ObservedGeneration = reg.Generation
		if got.host != "" {
			current.Host = got.host
		}
		if got.projectID != 0 {
			current.ProjectID = got.projectID
		}
		if got.summary != nil {
			current.UsedBytes, current.HardBytes, current.RepositoryCount = got.summary.UsedBytes, got.summary.HardBytes, got.summary.RepositoryCount
		}
		meta.SetStatusCondition(&current.Conditions, metav1.Condition{Type: conditionReady, Status: status, Reason: out.reason, Message: out.message, ObservedGeneration: reg.Generation})
	})
}

func registriesOfSetting(c client.Client) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list v1alpha1.RegistryList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return requests
	}
}

func registrySetting(obj client.Object) bool {
	switch obj.GetName() {
	case "registry.host", "registry.default-quota-gb", "registry.reserved-names", "registry.reserved-prefixes":
		return true
	}
	return false
}

func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	changed := predicate.Or[client.Object](predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}, deletionStarted())
	return ctrl.NewControllerManagedBy(mgr).Named("registry").
		For(&v1alpha1.Registry{}, builder.WithPredicates(changed)).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(registriesOfSetting(r.Client)), builder.WithPredicates(predicate.NewPredicateFuncs(registrySetting))).
		Watches(&v1alpha1.Addon{}, handler.EnqueueRequestsFromMapFunc(registriesOfSetting(r.Client)), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func deletionStarted() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectOld.GetDeletionTimestamp().IsZero() && !e.ObjectNew.GetDeletionTimestamp().IsZero()
	}}
}
