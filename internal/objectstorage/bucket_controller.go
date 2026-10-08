package objectstorage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	conditionQuotaApplied = "QuotaApplied"
	quotaChangeMessage    = "Rook applies the bucket quota when it creates the bucket; delete and create the bucket to change it"
	ownerChangeMessage    = "the owner of a bucket cannot change; create a new bucket"
)

type BucketReconciler struct {
	Client   client.Client
	S3       *s3.Client
	Endpoint string
}

type bucketState struct {
	outcome
	bucketName   string
	endpoint     string
	quotaChanged bool
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
	tenant, project, found, err := projectOf(ctx, r.Client, bucket.Namespace)
	if err != nil {
		return bucketState{}, err
	}
	if !found {
		return blocked(reasonNamespaceNotProj, "the namespace carries no tenant and project labels", steadyRequeue), nil
	}
	creds, problem, err := r.ownerCredentials(ctx, bucket)
	if err != nil || problem.reason != "" {
		return bucketState{outcome: problem}, err
	}
	if problem, err := storeProblem(ctx, r.Client); err != nil || problem.reason != "" {
		return bucketState{outcome: problem}, err
	}
	return r.bindClaim(ctx, bucket, tenant, project, creds)
}

func blocked(reason, message string, requeue time.Duration) bucketState {
	return bucketState{outcome: outcome{reason: reason, message: message, requeue: requeue}}
}

func (r *BucketReconciler) ownerCredentials(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) (s3.Credentials, outcome, error) {
	var user v1alpha1.ObjectStorageUser
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: bucket.Namespace, Name: bucket.Spec.UserRef}, &user)
	if apierrors.IsNotFound(err) {
		return s3.Credentials{}, outcome{reason: "UserNotFound", message: "no ObjectStorageUser " + bucket.Spec.UserRef + " in this namespace", requeue: pendingRequeue}, nil
	}
	if err != nil {
		return s3.Credentials{}, outcome{}, err
	}
	notReady := outcome{reason: "UserNotReady", message: "the ObjectStorageUser " + user.Name + " is not ready", requeue: pendingRequeue}
	if !v1alpha1.IsConditionTrue(user.Status.Conditions, v1alpha1.ConditionReady) || user.Status.CredentialsSecret == "" {
		return s3.Credentials{}, notReady, nil
	}
	var secret corev1.Secret
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: bucket.Namespace, Name: user.Status.CredentialsSecret}, &secret)
	if apierrors.IsNotFound(err) {
		return s3.Credentials{}, notReady, nil
	}
	if err != nil {
		return s3.Credentials{}, outcome{}, err
	}
	return s3.Credentials{AccessKey: string(secret.Data[AccessKeyKey]), SecretKey: string(secret.Data[SecretKeyKey])}, outcome{}, nil
}

func (r *BucketReconciler) bindClaim(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, tenant, project string, creds s3.Credentials) (bucketState, error) {
	desired, err := BucketClaim(bucket, tenant, project)
	if errors.Is(err, ErrQuotaInvalid) {
		return blocked(reasonQuotaInvalid, err.Error(), steadyRequeue), nil
	}
	if err != nil {
		return bucketState{}, err
	}
	claim, exists, err := getRook(ctx, r.Client, objectBucketClaimGVK, client.ObjectKeyFromObject(bucket))
	if err != nil {
		return bucketState{}, err
	}
	if !exists {
		if err := ssa.Apply(ctx, r.Client, desired, v1alpha1.ObjectStorageFieldManager); err != nil {
			return bucketState{}, err
		}
		return blocked("ClaimPending", "the object bucket claim is not bound yet", pendingRequeue), nil
	}
	if configValue(claim, "bucketOwner") != configValue(desired, "bucketOwner") {
		state := blocked("OwnerChangeNotSupported", ownerChangeMessage, steadyRequeue)
		state.quotaChanged = quotaDiffers(claim, desired)
		return state, nil
	}
	state, err := r.bindBucket(ctx, bucket, claim, creds)
	state.quotaChanged = quotaDiffers(claim, desired)
	return state, err
}

func (r *BucketReconciler) bindBucket(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, claim *unstructured.Unstructured, creds s3.Credentials) (bucketState, error) {
	if rookPhase(claim) != "Bound" {
		return blocked("ClaimPending", "the object bucket claim is not bound yet", pendingRequeue), nil
	}
	var data corev1.ConfigMap
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(bucket), &data)
	if apierrors.IsNotFound(err) || data.Data["BUCKET_NAME"] == "" {
		return blocked("ClaimPending", "the bucket details are not published yet", pendingRequeue), client.IgnoreNotFound(err)
	}
	if err != nil {
		return bucketState{}, err
	}
	name := data.Data["BUCKET_NAME"]
	state := bucketState{bucketName: name, endpoint: "http://" + data.Data["BUCKET_HOST"] + ":" + data.Data["BUCKET_PORT"]}
	state.outcome, err = r.applyS3(ctx, bucket, creds, name)
	return state, err
}

func (r *BucketReconciler) applyS3(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, creds s3.Credentials, name string) (outcome, error) {
	if err := ValidatePolicy(bucket.Spec.Policy); err != nil {
		return outcome{reason: "PolicyInvalid", message: err.Error(), requeue: steadyRequeue}, nil
	}
	if failed, err := r.applyVersioning(ctx, bucket, creds, name); failed != nil || err != nil {
		return outcome{reason: "VersioningFailed", message: messageOf(failed), requeue: pendingRequeue}, err
	}
	if failed, err := r.applyPolicy(ctx, bucket, creds, name); failed != nil || err != nil {
		return outcome{reason: "PolicyRejected", message: messageOf(failed), requeue: pendingRequeue}, err
	}
	return outcome{reason: reasonBound, requeue: steadyRequeue}, nil
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

func configValue(claim *unstructured.Unstructured, key string) string {
	value, _, _ := unstructured.NestedString(claim.Object, "spec", "additionalConfig", key)
	return value
}

func quotaDiffers(have, want *unstructured.Unstructured) bool {
	return configValue(have, "bucketMaxSize") != configValue(want, "bucketMaxSize") ||
		configValue(have, "bucketMaxObjects") != configValue(want, "bucketMaxObjects")
}

func (r *BucketReconciler) report(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, state bucketState) ctrl.Result {
	err := updateStatus(ctx, r.Client, bucket, func(latest *v1alpha1.ObjectStorageBucket) {
		latest.Status.ObservedGeneration = latest.Generation
		if state.bucketName != "" {
			latest.Status.BucketName = state.bucketName
			latest.Status.Endpoint = state.endpoint
		}
		v1alpha1.SetCondition(&latest.Status.Conditions, condition(v1alpha1.ConditionReady, state.ready(), state.reason, state.message, latest.Generation))
		v1alpha1.SetCondition(&latest.Status.Conditions, quotaCondition(state.quotaChanged, latest.Generation))
	})
	if err != nil {
		logf.FromContext(ctx).Info("status write failed", "error", err.Error())
	}
	return ctrl.Result{RequeueAfter: state.requeue}
}

func quotaCondition(changed bool, generation int64) metav1.Condition {
	if changed {
		return condition(conditionQuotaApplied, false, "ChangeNotSupported", quotaChangeMessage, generation)
	}
	return condition(conditionQuotaApplied, true, "Applied", "", generation)
}

func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-bucket").For(&v1alpha1.ObjectStorageBucket{}).Complete(r)
}
