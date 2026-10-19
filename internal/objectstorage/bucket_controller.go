package objectstorage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

const conditionQuotaApplied = "QuotaApplied"

type BucketReconciler struct {
	Client   client.Client
	S3       *s3.Client
	Endpoint string
}

type bucketState struct {
	outcome
	bucketName string
	endpoint   string
}

func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var bucket v1alpha1.ObjectStorageBucket
	if err := r.Client.Get(ctx, req.NamespacedName, &bucket); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bucket.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	state, err := r.converge(ctx, &bucket)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.report(ctx, &bucket, state), nil
}

func (r *BucketReconciler) converge(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) (bucketState, error) {
	_, found, err := LoadProject(ctx, r.Client, bucket.Namespace)
	if err != nil {
		return bucketState{}, err
	}
	if !found {
		return blocked(reasonNamespaceNotProj, "the namespace carries no tenant and project labels", steadyRequeue), nil
	}
	if problem, err := storeProblem(ctx, r.Client); err != nil || problem.reason != "" {
		return bucketState{outcome: problem}, err
	}
	return blocked("AccountPending", "the account model is not wired yet", pendingRequeue), nil
}

func blocked(reason, message string, requeue time.Duration) bucketState {
	return bucketState{outcome: outcome{reason: reason, message: message, requeue: requeue}}
}

func (r *BucketReconciler) applyS3(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, creds s3.Credentials, name string) (outcome, error) {
	if err := ValidatePolicy(bucket.Spec.Policy); err != nil {
		return outcome{reason: "PolicyInvalid", message: err.Error(), requeue: steadyRequeue}, nil
	}
	if failed, err := r.applyVersioning(ctx, bucket, creds, name); failed != nil || err != nil {
		return outcome{reason: failureReason(failed, "VersioningFailed"), message: messageOf(failed), requeue: pendingRequeue}, err
	}
	if failed, err := r.applyPolicy(ctx, bucket, creds, name); failed != nil || err != nil {
		return outcome{reason: failureReason(failed, "PolicyRejected"), message: messageOf(failed), requeue: pendingRequeue}, err
	}
	return outcome{reason: reasonBound, requeue: steadyRequeue}, nil
}

func failureReason(err error, refused string) string {
	if s3.IsUnavailable(err) {
		return reasonStoreNotReady
	}
	return refused
}

func messageOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (r *BucketReconciler) applyVersioning(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, creds s3.Credentials, name string) (failed, err error) {
	applied := bucket.Annotations[AppliedVersioningAnnotation]
	target := versioningTarget(bucket.Spec.Versioning, applied)
	if target == "" {
		return nil, nil
	}
	current, callErr := r.S3.GetVersioning(ctx, r.Endpoint, Region, creds, name)
	if callErr != nil {
		return callErr, nil
	}
	if current != target && (target == s3.VersioningEnabled || current == s3.VersioningEnabled) {
		if callErr := r.S3.PutVersioning(ctx, r.Endpoint, Region, creds, name, target); callErr != nil {
			return callErr, nil
		}
	}
	return nil, r.setAnnotation(ctx, bucket, AppliedVersioningAnnotation, target)
}

func versioningTarget(wanted bool, applied string) string {
	switch {
	case wanted:
		return s3.VersioningEnabled
	case applied == s3.VersioningEnabled:
		return s3.VersioningSuspended
	}
	return ""
}

func (r *BucketReconciler) applyPolicy(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, creds s3.Credentials, name string) (failed, err error) {
	wanted := bucket.Spec.Policy != ""
	if !wanted && bucket.Annotations[AppliedPolicyAnnotation] != "true" {
		return nil, nil
	}
	stored, found, callErr := r.S3.GetPolicy(ctx, r.Endpoint, Region, creds, name)
	if callErr != nil {
		return callErr, nil
	}
	if wanted {
		if !found || !samePolicy(stored, bucket.Spec.Policy) {
			if callErr := r.S3.PutPolicy(ctx, r.Endpoint, Region, creds, name, bucket.Spec.Policy); callErr != nil {
				return callErr, nil
			}
		}
		return nil, r.setAnnotation(ctx, bucket, AppliedPolicyAnnotation, "true")
	}
	if found {
		if callErr := r.S3.DeletePolicy(ctx, r.Endpoint, Region, creds, name); callErr != nil {
			return callErr, nil
		}
	}
	return nil, r.setAnnotation(ctx, bucket, AppliedPolicyAnnotation, "")
}

func samePolicy(stored, desired string) bool {
	var left, right any
	if json.Unmarshal([]byte(stored), &left) != nil || json.Unmarshal([]byte(desired), &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func (r *BucketReconciler) setAnnotation(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, key, value string) error {
	if bucket.Annotations[key] == value {
		return nil
	}
	var annotation any = value
	if value == "" {
		annotation = nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{key: annotation}}})
	if err != nil {
		return err
	}
	target := &v1alpha1.ObjectStorageBucket{}
	target.Name, target.Namespace = bucket.Name, bucket.Namespace
	if err := r.Client.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(v1alpha1.ObjectStorageFieldManager)); err != nil {
		return err
	}
	return nil
}

func (r *BucketReconciler) report(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, state bucketState) ctrl.Result {
	err := updateStatus(ctx, r.Client, bucket, func(latest *v1alpha1.ObjectStorageBucket) {
		latest.Status.ObservedGeneration = latest.Generation
		if state.bucketName != "" {
			latest.Status.BucketName = state.bucketName
			latest.Status.Endpoint = state.endpoint
		}
		v1alpha1.SetCondition(&latest.Status.Conditions, condition(v1alpha1.ConditionReady, state.ready(), state.reason, state.message, latest.Generation))
	})
	if err != nil {
		logf.FromContext(ctx).Info("status write failed", "error", err.Error())
	}
	return ctrl.Result{RequeueAfter: state.requeue}
}

func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-bucket").For(&v1alpha1.ObjectStorageBucket{}).Complete(r)
}
