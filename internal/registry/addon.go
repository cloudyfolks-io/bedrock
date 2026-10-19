package registry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/addons"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	ConditionRuntime = "RuntimeReady"
	steadyRequeue    = 5 * time.Minute
	pendingRequeue   = 15 * time.Second
)

var routeKinds = [][2]string{{"traefik.io/v1alpha1", "IngressRoute"}, {"traefik.io/v1alpha1", "Middleware"}}

type AddonReconciler struct {
	Client   client.Client
	S3       *s3.Client
	Endpoint string
	Random   io.Reader
}

type result struct {
	ready   bool
	reason  string
	message string
	requeue time.Duration
}

func (r *AddonReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var addon v1alpha1.Addon
	err := r.Client.Get(ctx, client.ObjectKey{Name: addons.Registry}, &addon)
	if apierrors.IsNotFound(err) || (err == nil && !addon.Spec.Enabled) {
		return ctrl.Result{RequeueAfter: steadyRequeue}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	out, err := r.converge(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.report(ctx, addon, out); err != nil {
		logf.FromContext(ctx).Error(err, "write the registry runtime condition")
	}
	return ctrl.Result{RequeueAfter: out.requeue}, nil
}

func (r *AddonReconciler) converge(ctx context.Context) (result, error) {
	values, err := settings.Effective(ctx, r.Client)
	if err != nil {
		return result{}, err
	}
	config, err := ParseConfig(values)
	if err != nil {
		return result{reason: "InvalidSettings", message: err.Error(), requeue: steadyRequeue}, nil
	}
	enabled, err := r.enabledAddons(ctx)
	if err != nil {
		return result{}, err
	}
	verdict := addons.RegistryCheck(addons.Facts{Settings: values, EnabledAddons: enabled, CertManagerReady: true})
	if verdict.State != addons.VerdictPassed {
		return result{reason: verdict.Reason, message: verdict.Message, requeue: pendingRequeue}, nil
	}
	for _, step := range r.steps(config) {
		if err := step(ctx); err != nil {
			return failure(err)
		}
	}
	return result{ready: true, reason: "Ready", message: "the registry runtime objects are applied", requeue: steadyRequeue}, nil
}

func (r *AddonReconciler) steps(config Config) []func(context.Context) error {
	apply := func(objects ...*unstructured.Unstructured) func(context.Context) error {
		return func(ctx context.Context) error { return r.apply(ctx, objects...) }
	}
	return []func(context.Context) error{
		apply(NamespaceObject()),
		func(ctx context.Context) error { return EnsureSecrets(ctx, r.Client, r.random()) },
		apply(TokenCertificate()),
		func(ctx context.Context) error {
			return ensureStore(ctx, r.Client, r.S3, r.Endpoint, imageStore(config))
		},
		func(ctx context.Context) error {
			return ensureStore(ctx, r.Client, r.S3, r.Endpoint, backupStore(config))
		},
		apply(DatabaseObjects(config, r.Endpoint)...),
		func(ctx context.Context) error { return r.applyRoutes(ctx, config) },
	}
}

func failure(err error) (result, error) {
	switch {
	case errors.Is(err, errStorePending):
		return result{reason: "StorePending", message: err.Error(), requeue: pendingRequeue}, nil
	case errors.Is(err, errNotOwned):
		return result{reason: "BucketNotOwned", message: err.Error(), requeue: pendingRequeue}, nil
	case meta.IsNoMatchError(err):
		return result{reason: "MissingCRD", message: err.Error(), requeue: pendingRequeue}, nil
	}
	return result{}, err
}

func (r *AddonReconciler) random() io.Reader {
	if r.Random != nil {
		return r.Random
	}
	return rand.Reader
}

func (r *AddonReconciler) apply(ctx context.Context, objects ...*unstructured.Unstructured) error {
	for _, obj := range objects {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.RegistryFieldManager); err != nil {
			return fmt.Errorf("apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}

func (r *AddonReconciler) applyRoutes(ctx context.Context, config Config) error {
	desired := RouteObjects(config)
	if err := r.apply(ctx, desired...); err != nil {
		return err
	}
	return r.pruneRoutes(ctx, desired)
}

func (r *AddonReconciler) pruneRoutes(ctx context.Context, desired []*unstructured.Unstructured) error {
	keep := map[string]bool{}
	for _, obj := range desired {
		keep[obj.GetKind()+"/"+obj.GetName()] = true
	}
	for _, apiKind := range routeKinds {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(apiKind[0])
		list.SetKind(apiKind[1] + "List")
		if err := r.Client.List(ctx, list, client.InNamespace(Namespace), client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
			return err
		}
		for i := range list.Items {
			item := &list.Items[i]
			if keep[item.GetKind()+"/"+item.GetName()] {
				continue
			}
			if err := client.IgnoreNotFound(r.Client.Delete(ctx, item)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *AddonReconciler) enabledAddons(ctx context.Context) (map[string]bool, error) {
	var list v1alpha1.AddonList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, addon := range list.Items {
		enabled[addon.Name] = addon.Spec.Enabled
	}
	return enabled, nil
}

func (r *AddonReconciler) report(ctx context.Context, addon v1alpha1.Addon, out result) error {
	status := metav1.ConditionFalse
	if out.ready {
		status = metav1.ConditionTrue
	}
	desired := metav1.Condition{Type: ConditionRuntime, Status: status, Reason: out.reason, Message: out.message, ObservedGeneration: addon.Generation}
	return setAddonCondition(ctx, r.Client, addon, desired)
}

func setAddonCondition(ctx context.Context, c client.Client, addon v1alpha1.Addon, desired metav1.Condition) error {
	if same(meta.FindStatusCondition(addon.Status.Conditions, desired.Type), desired) {
		return nil
	}
	conditions := []metav1.Condition{}
	v1alpha1.SetCondition(&conditions, desired)
	status := &v1alpha1.Addon{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Addon"},
		ObjectMeta: metav1.ObjectMeta{Name: addon.Name},
		Status:     v1alpha1.AddonStatus{CommonStatus: v1alpha1.CommonStatus{Conditions: conditions}},
	}
	return ssa.ApplyStatus(ctx, c, status, v1alpha1.RegistryFieldManager)
}

func same(live *metav1.Condition, desired metav1.Condition) bool {
	return live != nil && live.Status == desired.Status && live.Reason == desired.Reason && live.Message == desired.Message
}

func watchedSetting(obj client.Object) bool {
	name := obj.GetName()
	return strings.HasPrefix(name, "registry.") || name == "database.enabled" || name == "database.backup.bucket" || name == tlsModeKey
}

func (r *AddonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toRegistry := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: addons.Registry}}}
	})
	return ctrl.NewControllerManagedBy(mgr).Named("registry-addon").
		For(&v1alpha1.Addon{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.Setting{}, toRegistry, builder.WithPredicates(predicate.NewPredicateFuncs(watchedSetting))).
		Complete(r)
}

func SetupWithManager(mgr ctrl.Manager) error {
	runtime := &AddonReconciler{Client: mgr.GetClient(), S3: &s3.Client{}, Endpoint: objectstorage.Endpoint}
	if err := runtime.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&HarborReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&RulesConfigReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&RegistryReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	return (&CredentialReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr)
}
