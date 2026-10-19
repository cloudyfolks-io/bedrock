package container

import (
	"context"
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
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
	buildObserveInterval = 15 * time.Second
	buildGateRetry       = 30 * time.Second
)

type BuildReconciler struct {
	Client  client.Client
	Secrets client.Reader
	Scheme  *runtime.Scheme
	Now     func() time.Time
}

func (r *BuildReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *BuildReconciler) resolver() BuildResolver {
	return BuildResolver{Client: r.Client, Secrets: r.Secrets}
}

func (r *BuildReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b v1alpha1.Build
	if err := r.Client.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !b.DeletionTimestamp.IsZero() || IsTerminalBuildPhase(b.Status.Phase) {
		return ctrl.Result{}, nil
	}
	job, err := r.jobOf(ctx, b)
	var conflict nameConflict
	if errors.As(err, &conflict) {
		return r.report(ctx, b, gateOf(ReasonNameConflict, "%s", conflict.Error()), BuildDecision{}, "", false, buildGateRetry)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if job == nil && b.Status.JobName == "" {
		gate, created, err := r.start(ctx, b)
		if err != nil {
			return ctrl.Result{}, err
		}
		if gate != nil {
			return r.report(ctx, b, gate, BuildDecision{}, "", false, buildGateRetry)
		}
		return r.observe(ctx, b, created, created != nil)
	}
	return r.observe(ctx, b, job, job != nil)
}

func (r *BuildReconciler) jobOf(ctx context.Context, b v1alpha1.Build) (*batchv1.Job, error) {
	var job batchv1.Job
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Name}, &job)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(&job, &b) {
		return nil, nameConflict{Kind: "Job", Name: job.Name}
	}
	return &job, nil
}

func (r *BuildReconciler) start(ctx context.Context, b v1alpha1.Build) (*Gate, *batchv1.Job, error) {
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: b.Namespace}, &ns); err != nil {
		return nil, nil, err
	}
	labels, ok := BuildOwnerLabels(b.Name, ns)
	if !ok {
		return gateOf(ReasonNotInProject, "the namespace is not a project namespace"), nil, nil
	}
	in, gate, err := r.resolver().Resolve(ctx, b, labels)
	if err != nil || gate != nil {
		return gate, nil, err
	}
	rendered, err := RenderBuild(in)
	if err != nil {
		return gateOf(ReasonInvalidSpec, "%s", err.Error()), nil, nil
	}
	job, gate, err := r.create(ctx, rendered)
	return gate, job, err
}

func (r *BuildReconciler) create(ctx context.Context, rendered Rendered) (*batchv1.Job, *Gate, error) {
	err := r.applyRendered(ctx, rendered)
	var conflict nameConflict
	switch {
	case errors.As(err, &conflict):
		return nil, gateOf(ReasonNameConflict, "%s", conflict.Error()), nil
	case apierrors.IsInvalid(err) || apierrors.IsForbidden(err):
		logf.FromContext(ctx).Error(err, "the cluster refused an object of the build", "object", rendered.Job.GetName())
		return nil, gateOf(ReasonApplyFailed, "the cluster refused an object of the build"), nil
	case err != nil:
		return nil, nil, err
	}
	var job batchv1.Job
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Job.Object, &job); err != nil {
		return nil, nil, err
	}
	return &job, nil, nil
}

func (r *BuildReconciler) applyRendered(ctx context.Context, rendered Rendered) error {
	if rendered.ConfigMap != nil {
		if err := r.applyOwnedConfigMap(ctx, rendered.ConfigMap); err != nil {
			return fmt.Errorf("apply the mirror config: %w", err)
		}
	}
	err := r.Client.Create(ctx, rendered.Job.DeepCopy(), client.FieldOwner(v1alpha1.ContainerFieldManager))
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (r *BuildReconciler) applyOwnedConfigMap(ctx context.Context, desired *unstructured.Unstructured) error {
	reader := r.Secrets
	if reader == nil {
		reader = r.Client
	}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := reader.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if err == nil && !controlledBy(existing, desired) {
		return nameConflict{Kind: desired.GetKind(), Name: desired.GetName()}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return ssa.Apply(ctx, r.Client, desired, v1alpha1.ContainerFieldManager)
}

func (r *BuildReconciler) observe(ctx context.Context, b v1alpha1.Build, job *batchv1.Job, jobExists bool) (ctrl.Result, error) {
	pods, err := r.podsOf(ctx, b)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := r.now()
	decision := DecideBuild(BuildObservation{Job: job, Pods: pods, Now: now, JobMissingSince: JobMissingSince(b)})
	if err := r.writeStatus(ctx, b, nil, decision, b.Name, jobExists); err != nil {
		return ctrl.Result{}, fmt.Errorf("write the build status: %w", err)
	}
	if decision.Terminal() {
		return ctrl.Result{}, r.stopJob(ctx, job)
	}
	return ctrl.Result{RequeueAfter: buildObserveInterval}, nil
}

func (r *BuildReconciler) report(ctx context.Context, b v1alpha1.Build, gate *Gate, decision BuildDecision, jobName string, jobExists bool, after time.Duration) (ctrl.Result, error) {
	if err := r.writeStatus(ctx, b, gate, decision, jobName, jobExists); err != nil {
		return ctrl.Result{}, fmt.Errorf("write the build status: %w", err)
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *BuildReconciler) podsOf(ctx context.Context, b v1alpha1.Build) ([]corev1.Pod, error) {
	var list corev1.PodList
	if err := r.Client.List(ctx, &list, client.InNamespace(b.Namespace), client.MatchingLabels(BuildSelectorLabels(b.Name))); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *BuildReconciler) stopJob(ctx context.Context, job *batchv1.Job) error {
	if job == nil || jobFinished(*job) {
		return nil
	}
	err := r.Client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
	return client.IgnoreNotFound(err)
}

func jobFinished(job batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *BuildReconciler) writeStatus(ctx context.Context, b v1alpha1.Build, gate *Gate, decision BuildDecision, jobName string, jobExists bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Build
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&b), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if IsTerminalBuildPhase(current.Status.Phase) {
			return nil
		}
		desired := current.DeepCopy()
		desired.Status = BuildStatusOf(current, gate, decision, jobNameOf(current, jobName), jobExists, r.now())
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.ContainerFieldManager))
	})
}

func jobNameOf(current v1alpha1.Build, proposed string) string {
	if current.Status.JobName != "" {
		return current.Status.JobName
	}
	return proposed
}
