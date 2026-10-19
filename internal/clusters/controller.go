package clusters

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	applyFailureRetry = 30 * time.Second
	provisionRetry    = 10 * time.Second
	observeInterval   = time.Minute
	tokenGrace        = 15 * time.Minute
)

type Reconciler struct {
	Client    client.Client
	Secrets   client.Reader
	Reader    client.Reader
	FabricIPs client.Reader
	Scheme    *runtime.Scheme
	Now       func() time.Time
}

type outcome struct {
	Gate         *Gate
	RequeueAfter time.Duration
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cluster v1alpha1.KubernetesCluster
	if err := r.Client.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cluster.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	done, err := r.converge(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	observed, err := r.observe(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, cluster, done.Gate, observed); err != nil {
		return ctrl.Result{}, fmt.Errorf("write the cluster status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueAfter(done, observed, cluster.Spec.Suspended)}, nil
}

func requeueAfter(done outcome, observed Observed, suspended bool) time.Duration {
	if done.RequeueAfter > 0 {
		return done.RequeueAfter
	}
	if DecideCluster(observed.ControlPlane, observed.Workers, suspended).Phase == v1alpha1.KubernetesClusterPhaseProvisioning {
		return provisionRetry
	}
	return observeInterval
}

func (r *Reconciler) resolver() Resolver {
	return Resolver{Client: r.Client, Reader: r.reader(), FabricIPs: r.FabricIPs}
}

func (r *Reconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

func (r *Reconciler) secretReader() client.Reader {
	if r.Secrets != nil {
		return r.Secrets
	}
	return r.Client
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func OwnerLabels(name string, ns corev1.Namespace) (map[string]string, bool) {
	tenant, project := ns.Labels[v1alpha1.LabelTenant], ns.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return nil, false
	}
	return v1alpha1.TenantLabels(Kind, name, tenant, project), true
}

func (r *Reconciler) converge(ctx context.Context, cluster v1alpha1.KubernetesCluster) (outcome, error) {
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: cluster.Namespace}, &ns); err != nil {
		return outcome{}, err
	}
	labels, ok := OwnerLabels(cluster.Name, ns)
	if !ok {
		return outcome{Gate: gateOf(ReasonNotInProject, "the namespace is not a project namespace")}, nil
	}
	if issues := ValidateCluster(cluster); len(issues) > 0 {
		return outcome{Gate: gateOf(ReasonInvalidSpec, "%s", IssueMessage(issues))}, nil
	}
	resolved, gate, err := r.resolver().Resolve(ctx, cluster)
	if err != nil || gate != nil {
		return outcome{Gate: gate, RequeueAfter: retryFor(gate)}, err
	}
	if resolved.AddressIsNew {
		if err := r.recordAddress(ctx, cluster, resolved.Address); err != nil {
			return outcome{}, fmt.Errorf("record the control plane address: %w", err)
		}
	}
	return r.provision(ctx, cluster, labels, resolved.Address)
}

func retryFor(gate *Gate) time.Duration {
	if gate == nil {
		return 0
	}
	switch gate.Reason {
	case ReasonInvalidSpec, ReasonNotInProject, ReasonVersionNotAllowed, ReasonCIDROverlap:
		return 0
	}
	return applyFailureRetry
}

func (r *Reconciler) recordAddress(ctx context.Context, cluster v1alpha1.KubernetesCluster, address string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.KubernetesCluster
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(&cluster), &current); err != nil {
			return err
		}
		if current.Status.ControlPlane.Address != "" {
			return nil
		}
		current.Status.ControlPlane.Address = address
		return r.Client.Status().Update(ctx, &current, client.FieldOwner(v1alpha1.KubernetesClusterFieldManager))
	})
}

type nameConflict struct{ Kind, Name string }

func (e nameConflict) Error() string {
	return fmt.Sprintf("the name of the %s %q is used by an object that this resource does not own", e.Kind, e.Name)
}

func (r *Reconciler) provision(ctx context.Context, cluster v1alpha1.KubernetesCluster, labels map[string]string, address string) (outcome, error) {
	done, err := r.provisionAll(ctx, cluster, labels, address)
	var conflict nameConflict
	if errors.As(err, &conflict) {
		return outcome{Gate: gateOf(ReasonNameConflict, "%s", conflict.Error()), RequeueAfter: applyFailureRetry}, nil
	}
	if apierrors.IsInvalid(err) || apierrors.IsForbidden(err) {
		logf.FromContext(ctx).Error(err, "the cluster refused an object of the kubernetes cluster", "cluster", client.ObjectKeyFromObject(&cluster))
		return outcome{Gate: gateOf(ReasonApplyFailed, "the cluster refused an object of the kubernetes cluster"), RequeueAfter: applyFailureRetry}, nil
	}
	return done, err
}

func (r *Reconciler) provisionAll(ctx context.Context, cluster v1alpha1.KubernetesCluster, labels map[string]string, address string) (outcome, error) {
	controlPlane, err := RenderControlPlane(ControlPlaneInput{Cluster: cluster, Labels: labels, Address: address})
	if err != nil {
		return outcome{}, err
	}
	if err := r.applyOwned(ctx, controlPlane, cluster); err != nil {
		return outcome{}, fmt.Errorf("apply the control plane: %w", err)
	}
	view, err := r.observeControlPlane(ctx, cluster)
	if err != nil || !view.Ready {
		return outcome{RequeueAfter: provisionRetry}, err
	}
	return r.syncWorkers(ctx, cluster, labels)
}

func (r *Reconciler) applyOwned(ctx context.Context, desired *unstructured.Unstructured, owner v1alpha1.KubernetesCluster) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if err == nil && !metav1.IsControlledBy(existing, &owner) {
		return nameConflict{Kind: desired.GetKind(), Name: desired.GetName()}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return ssa.Apply(ctx, r.Client, desired, v1alpha1.KubernetesClusterFieldManager)
}

func (r *Reconciler) writeStatus(ctx context.Context, cluster v1alpha1.KubernetesCluster, gate *Gate, observed Observed) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.KubernetesCluster
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(&cluster), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		desired.Status = StatusOf(current, gate, observed, current.Status.ControlPlane.Address, r.now())
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.KubernetesClusterFieldManager))
	})
}
