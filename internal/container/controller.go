package container

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	applyFailureRetry = 30 * time.Second
	observeInterval   = time.Minute
)

var (
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	autoscalerGVK = schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"}
)

type Reconciler struct {
	Client    client.Client
	Secrets   client.Reader
	FabricIPs client.Reader
	Scheme    *runtime.Scheme
	Now       func() time.Time
}

type outcome struct {
	Gate         *Gate
	RequeueAfter time.Duration
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var c v1alpha1.Container
	if err := r.Client.Get(ctx, req.NamespacedName, &c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !c.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	done, err := r.converge(ctx, c)
	if err != nil {
		return ctrl.Result{}, err
	}
	decided, err := r.observe(ctx, c)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, c, done.Gate, decided); err != nil {
		return ctrl.Result{}, fmt.Errorf("write the container status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueAfter(done, decided)}, nil
}

func requeueAfter(done outcome, decided Status) time.Duration {
	if done.RequeueAfter > 0 {
		return done.RequeueAfter
	}
	if decided.Phase == v1alpha1.ContainerPhasePending {
		return applyFailureRetry
	}
	return observeInterval
}

func (r *Reconciler) resolver() Resolver {
	return Resolver{Client: r.Client, Secrets: r.Secrets, FabricIPs: r.FabricIPs}
}

func (r *Reconciler) converge(ctx context.Context, c v1alpha1.Container) (outcome, error) {
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: c.Namespace}, &ns); err != nil {
		return outcome{}, err
	}
	labels, ok := OwnerLabels(c.Name, ns)
	if !ok {
		return outcome{Gate: gateOf(ReasonNotInProject, "the namespace is not a project namespace")}, nil
	}
	if issues := ValidateContainer(c); len(issues) > 0 {
		return outcome{Gate: gateOf(ReasonInvalidSpec, "%s", IssueMessage(issues))}, nil
	}
	in, gate, err := r.resolver().Resolve(ctx, c, labels)
	if err != nil || gate != nil {
		return outcome{Gate: gate, RequeueAfter: retryFor(gate)}, err
	}
	return r.apply(ctx, in)
}

func retryFor(gate *Gate) time.Duration {
	if gate == nil {
		return 0
	}
	switch gate.Reason {
	case ReasonAddressInUse, ReasonWaitingForDefaultGroup, ReasonVolumeInUse, ReasonRuntimeUnavailable, ReasonSubnetNotReady, ReasonSecurityGroupNotReady:
		return applyFailureRetry
	}
	return 0
}

type nameConflict struct{ Kind, Name string }

func (e nameConflict) Error() string {
	return fmt.Sprintf("the name of the %s %q is used by an object that this resource does not own", e.Kind, e.Name)
}

func (r *Reconciler) apply(ctx context.Context, in RenderInput) (outcome, error) {
	err := r.applyRendered(ctx, in)
	var conflict nameConflict
	if errors.As(err, &conflict) {
		return outcome{Gate: gateOf(ReasonNameConflict, "%s", conflict.Error()), RequeueAfter: applyFailureRetry}, nil
	}
	if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
		logf.FromContext(ctx).Error(err, "the cluster refused an object of the container", "container", client.ObjectKeyFromObject(&in.Container))
		return outcome{Gate: gateOf(ReasonApplyFailed, "the cluster refused an object of the container"), RequeueAfter: applyFailureRetry}, nil
	}
	return outcome{}, err
}

func (r *Reconciler) applyRendered(ctx context.Context, in RenderInput) error {
	deployment, err := RenderDeployment(in)
	if err != nil {
		return err
	}
	autoscaler, err := RenderAutoscaler(in)
	if err != nil {
		return err
	}
	if err := r.applyOwned(ctx, deployment); err != nil {
		return fmt.Errorf("apply the deployment: %w", err)
	}
	if autoscaler == nil {
		return r.deleteAutoscaler(ctx, in.Container)
	}
	if err := r.applyOwned(ctx, autoscaler); err != nil {
		return fmt.Errorf("apply the autoscaler: %w", err)
	}
	return nil
}

func (r *Reconciler) applyOwned(ctx context.Context, desired *unstructured.Unstructured) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if err == nil && !controlledBy(existing, desired) {
		return nameConflict{Kind: desired.GetKind(), Name: desired.GetName()}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return ssa.Apply(ctx, r.Client, desired, v1alpha1.ContainerFieldManager)
}

func controlledBy(existing, desired *unstructured.Unstructured) bool {
	want := metav1.GetControllerOf(desired)
	have := metav1.GetControllerOf(existing)
	return want != nil && have != nil && want.UID == have.UID
}

func (r *Reconciler) deleteAutoscaler(ctx context.Context, c v1alpha1.Container) error {
	existing := &autoscalingv2.HorizontalPodAutoscaler{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: c.Name}, existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner := metav1.GetControllerOf(existing); owner == nil || owner.UID != c.UID {
		return nil
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, existing))
}

func (r *Reconciler) observe(ctx context.Context, c v1alpha1.Container) (Status, error) {
	var deployment appsv1.Deployment
	var observed *appsv1.Deployment
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: c.Name}, &deployment)
	switch {
	case err == nil && metav1.IsControlledBy(&deployment, &c):
		observed = &deployment
	case err != nil && !apierrors.IsNotFound(err):
		return Status{}, err
	}
	pods, err := network.PodsOfContainer(ctx, r.Client, c)
	if err != nil {
		return Status{}, err
	}
	views := make([]PodView, 0, len(pods))
	for _, pod := range pods {
		views = append(views, PodViewOf(pod, SubnetName(c.Spec)))
	}
	return Decide(DeploymentStatusOf(c, observed), views), nil
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) writeStatus(ctx context.Context, c v1alpha1.Container, gate *Gate, decided Status) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Container
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&c), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		desired.Status = StatusOf(current, gate, decided, current.Name, r.now())
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.ContainerFieldManager))
	})
}
