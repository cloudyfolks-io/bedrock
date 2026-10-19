package objectstorage

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/admin"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const (
	conditionQuotaApplied = "QuotaApplied"
	defaultMaxPurgePages  = 100000
	legacyBucketMessage   = "this bucket was made by an older release; delete the ObjectStorageBucket and create it again"
)

var bucketFinalizer = platform.Finalizer("ObjectStorageBucket")

type BucketReconciler struct {
	Client        client.Client
	S3            *s3.Client
	Endpoint      string
	Admin         *admin.Client
	Rules         func(context.Context) (Rules, error)
	MaxPurgePages int
	Now           func() time.Time
}

type bucketState struct {
	outcome
	bucketName string
	endpoint   string
	seen       observation
}

func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var bucket v1alpha1.ObjectStorageBucket
	if err := r.Client.Get(ctx, req.NamespacedName, &bucket); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bucket.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &bucket)
	}
	if err := r.ensureFinalizer(ctx, &bucket); err != nil {
		return ctrl.Result{}, err
	}
	state, err := r.converge(ctx, &bucket)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.report(ctx, &bucket, state), nil
}

func (r *BucketReconciler) ensureFinalizer(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) error {
	if controllerutil.ContainsFinalizer(bucket, bucketFinalizer) {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.ObjectStorageBucket{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(bucket), latest); err != nil {
			return err
		}
		controllerutil.AddFinalizer(latest, bucketFinalizer)
		if err := r.Client.Update(ctx, latest, client.FieldOwner(v1alpha1.ObjectStorageFieldManager)); err != nil {
			return err
		}
		*bucket = *latest
		return nil
	})
}

func blocked(reason, message string, requeue time.Duration) bucketState {
	return bucketState{outcome: outcome{reason: reason, message: message, requeue: requeue}}
}

func (r *BucketReconciler) converge(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) (bucketState, error) {
	project, found, err := LoadProject(ctx, r.Client, bucket.Namespace)
	if err != nil {
		return bucketState{}, err
	}
	if !found {
		return blocked(reasonNamespaceNotProj, "the namespace carries no tenant and project labels", steadyRequeue), nil
	}
	if legacy, err := r.hasOldClaim(ctx, bucket); err != nil || legacy {
		return blocked(reasonLegacyBucket, legacyBucketMessage, steadyRequeue), err
	}
	rules, err := r.rules(ctx)
	if err != nil {
		return bucketState{}, err
	}
	name := S3BucketName(bucket)
	if reason := nameProblem(name, rules, bucket.Annotations[ReservedNameApprovedAnnotation] == "true"); reason != "" {
		return blocked(reason, fmt.Sprintf("the bucket name %q is not allowed", name), steadyRequeue), nil
	}
	if problem, err := storeProblem(ctx, r.Client); err != nil || problem.reason != "" {
		return bucketState{outcome: problem}, err
	}
	if refused, err := r.projectRefusal(ctx, bucket, name, rules); err != nil || refused.reason != "" {
		return refused, err
	}
	account, ready, err := EnsureAccount(ctx, r.Client, project)
	if err != nil || !ready {
		return bucketState{outcome: accountPending()}, err
	}
	state := bucketState{bucketName: name, endpoint: r.Endpoint}
	state.outcome, err = r.ensureBucket(ctx, account.Root, name)
	if err != nil || state.reason != "" {
		state.bucketName, state.endpoint = "", ""
		return state, err
	}
	state.outcome, err = r.applyS3(ctx, bucket, account.Root, name, rules)
	if err != nil || !state.ready() || r.Admin == nil {
		return state, err
	}
	state.seen, err = r.observe(ctx, bucket, name)
	return state, err
}

func (r *BucketReconciler) hasOldClaim(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) (bool, error) {
	_, found, err := getRook(ctx, r.Client, objectBucketClaimGVK, client.ObjectKeyFromObject(bucket))
	return found, err
}

func (r *BucketReconciler) projectRefusal(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, name string, rules Rules) (bucketState, error) {
	var buckets v1alpha1.ObjectStorageBucketList
	if err := r.Client.List(ctx, &buckets, client.InNamespace(bucket.Namespace)); err != nil {
		return bucketState{}, err
	}
	others := []v1alpha1.ObjectStorageBucket{}
	for _, other := range buckets.Items {
		if other.Name != bucket.Name && other.DeletionTimestamp.IsZero() {
			others = append(others, other)
		}
	}
	if usesName(others, bucket, name) {
		return blocked(reasonNameTaken, fmt.Sprintf("another bucket of this project uses the name %q", name), steadyRequeue), nil
	}
	if bucket.Status.BucketName == "" && rank(others, bucket) >= rules.MaxBuckets {
		return blocked(reasonBucketLimit, fmt.Sprintf("the project holds %d buckets already", rules.MaxBuckets), steadyRequeue), nil
	}
	return bucketState{}, nil
}

func comesBefore(a, b *v1alpha1.ObjectStorageBucket) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

func usesName(others []v1alpha1.ObjectStorageBucket, bucket *v1alpha1.ObjectStorageBucket, name string) bool {
	for i := range others {
		if S3BucketName(&others[i]) == name && comesBefore(&others[i], bucket) {
			return true
		}
	}
	return false
}

func rank(others []v1alpha1.ObjectStorageBucket, bucket *v1alpha1.ObjectStorageBucket) int {
	count := 0
	for i := range others {
		if others[i].Status.BucketName != "" || comesBefore(&others[i], bucket) {
			count++
		}
	}
	return count
}

func (r *BucketReconciler) ensureBucket(ctx context.Context, root s3.Credentials, name string) (outcome, error) {
	err := r.S3.HeadBucket(ctx, r.Endpoint, Region, root, name)
	switch {
	case err == nil:
		return outcome{}, nil
	case s3.IsCode(err, s3.AccessDenied):
		return takenOutcome(name), nil
	case !s3.IsCode(err, s3.NoSuchBucket):
		return bucketFailure(err), nil
	}
	err = r.S3.CreateBucket(ctx, r.Endpoint, Region, root, name)
	switch {
	case err == nil, s3.IsCode(err, s3.BucketAlreadyOwnedByYou):
		return outcome{}, nil
	case s3.IsCode(err, s3.BucketAlreadyExists):
		return takenOutcome(name), nil
	}
	return bucketFailure(err), nil
}

func takenOutcome(name string) outcome {
	return outcome{reason: reasonNameTaken, message: fmt.Sprintf("the bucket name %q belongs to another project", name), requeue: steadyRequeue}
}

func bucketFailure(err error) outcome {
	return outcome{reason: failureReason(err, "BucketFailed"), message: err.Error(), requeue: pendingRequeue}
}

func (r *BucketReconciler) applyS3(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, root s3.Credentials, name string, rules Rules) (outcome, error) {
	if err := ValidatePolicy(bucket.Spec.Policy); err != nil {
		return outcome{reason: "PolicyInvalid", message: err.Error(), requeue: steadyRequeue}, nil
	}
	if failed, err := r.applyCORS(ctx, bucket, root, name, rules); failed != nil || err != nil {
		return outcome{reason: failureReason(failed, "CorsFailed"), message: messageOf(failed), requeue: pendingRequeue}, err
	}
	if failed, err := r.applyVersioning(ctx, bucket, root, name); failed != nil || err != nil {
		return outcome{reason: failureReason(failed, "VersioningFailed"), message: messageOf(failed), requeue: pendingRequeue}, err
	}
	if failed, err := r.applyPolicy(ctx, bucket, root, name); failed != nil || err != nil {
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

func (r *BucketReconciler) applyCORS(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, root s3.Credentials, name string, rules Rules) (failed, err error) {
	desired := mergeOrigins(rules.CORSOrigins, bucket.Spec.CORSOrigins)
	joined := strings.Join(desired, ",")
	if joined == bucket.Annotations[AppliedCORSAnnotation] {
		return nil, nil
	}
	var callErr error
	if len(desired) == 0 {
		callErr = r.S3.DeleteBucketCors(ctx, r.Endpoint, Region, root, name)
	} else {
		callErr = r.S3.PutBucketCors(ctx, r.Endpoint, Region, root, name, desired)
	}
	if callErr != nil {
		return callErr, nil
	}
	return nil, r.setAnnotation(ctx, bucket, AppliedCORSAnnotation, joined)
}

func (r *BucketReconciler) applyVersioning(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, root s3.Credentials, name string) (failed, err error) {
	applied := bucket.Annotations[AppliedVersioningAnnotation]
	target := versioningTarget(bucket.Spec.Versioning, applied)
	if target == "" {
		return nil, nil
	}
	current, callErr := r.S3.GetVersioning(ctx, r.Endpoint, Region, root, name)
	if callErr != nil {
		return callErr, nil
	}
	if current != target && (target == s3.VersioningEnabled || current == s3.VersioningEnabled) {
		if callErr := r.S3.PutVersioning(ctx, r.Endpoint, Region, root, name, target); callErr != nil {
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

func (r *BucketReconciler) applyPolicy(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, root s3.Credentials, name string) (failed, err error) {
	wanted := bucket.Spec.Policy != ""
	if !wanted && bucket.Annotations[AppliedPolicyAnnotation] != "true" {
		return nil, nil
	}
	stored, found, callErr := r.S3.GetPolicy(ctx, r.Endpoint, Region, root, name)
	if callErr != nil {
		return callErr, nil
	}
	if wanted {
		if !found || !samePolicy(stored, bucket.Spec.Policy) {
			if callErr := r.S3.PutPolicy(ctx, r.Endpoint, Region, root, name, bucket.Spec.Policy); callErr != nil {
				return callErr, nil
			}
		}
		return nil, r.setAnnotation(ctx, bucket, AppliedPolicyAnnotation, "true")
	}
	if found {
		if callErr := r.S3.DeletePolicy(ctx, r.Endpoint, Region, root, name); callErr != nil {
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
	return r.Client.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(v1alpha1.ObjectStorageFieldManager))
}

func (r *BucketReconciler) finalize(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(bucket, bucketFinalizer) {
		return ctrl.Result{}, nil
	}
	state, err := r.release(ctx, bucket)
	if err != nil {
		return ctrl.Result{}, err
	}
	if state.reason != "" {
		return r.report(ctx, bucket, state), nil
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.ObjectStorageBucket{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(bucket), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		controllerutil.RemoveFinalizer(latest, bucketFinalizer)
		return client.IgnoreNotFound(r.Client.Update(ctx, latest, client.FieldOwner(v1alpha1.ObjectStorageFieldManager)))
	})
	return ctrl.Result{}, err
}

func (r *BucketReconciler) release(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket) (bucketState, error) {
	name := S3BucketName(bucket)
	if bucket.Status.BucketName != name {
		return bucketState{}, nil
	}
	account, exists, err := r.accountOf(ctx, bucket.Namespace)
	if err != nil || !exists {
		return bucketState{}, err
	}
	if account.ID == "" {
		return blocked(reasonAccountPending, "the account of this project is not ready, so the bucket cannot be deleted yet", pendingRequeue), nil
	}
	force := bucket.Spec.ForceDestroy || r.namespaceLeaving(ctx, bucket.Namespace)
	return r.empty(ctx, account.Root, name, force)
}

func (r *BucketReconciler) accountOf(ctx context.Context, namespace string) (Account, bool, error) {
	account, ready, err := ReadAccount(ctx, r.Client, namespace)
	if err != nil || ready {
		return account, true, err
	}
	_, found, err := getRook(ctx, r.Client, cephObjectStoreAccountGVK, client.ObjectKey{Namespace: StoreNamespace, Name: AccountName(namespace)})
	return Account{}, found, err
}

func (r *BucketReconciler) namespaceLeaving(ctx context.Context, namespace string) bool {
	ns := namespaceMeta()
	err := r.Client.Get(ctx, client.ObjectKey{Name: namespace}, ns)
	return apierrors.IsNotFound(err) || err == nil && !ns.DeletionTimestamp.IsZero()
}

func (r *BucketReconciler) empty(ctx context.Context, root s3.Credentials, name string, force bool) (bucketState, error) {
	page, err := r.S3.ListObjectVersions(ctx, r.Endpoint, Region, root, name, "", "")
	if s3.IsCode(err, s3.NoSuchBucket) {
		return bucketState{}, nil
	}
	if err != nil {
		return bucketState{outcome: bucketFailure(err)}, nil
	}
	if len(page.Objects) > 0 && !force {
		return blocked(reasonNotEmpty, notEmptyMessage(page), steadyRequeue), nil
	}
	if len(page.Objects) > 0 {
		if state := r.purge(ctx, root, name, page); state.reason != "" {
			return state, nil
		}
	}
	err = r.S3.DeleteBucket(ctx, r.Endpoint, Region, root, name)
	switch {
	case err == nil, s3.IsCode(err, s3.NoSuchBucket):
		return bucketState{}, nil
	case s3.IsCode(err, s3.BucketNotEmpty):
		return blocked(reasonNotEmpty, "the bucket still holds objects or unfinished uploads", steadyRequeue), nil
	}
	return bucketState{outcome: bucketFailure(err)}, nil
}

func notEmptyMessage(page s3.VersionPage) string {
	count := fmt.Sprintf("%d", len(page.Objects))
	if page.Truncated {
		count = "at least " + count
	}
	return "the bucket holds " + count + " object versions; set forceDestroy to delete it with its objects"
}

func (r *BucketReconciler) purge(ctx context.Context, root s3.Credentials, name string, first s3.VersionPage) bucketState {
	limit := r.MaxPurgePages
	if limit <= 0 {
		limit = defaultMaxPurgePages
	}
	page := first
	for deleted := 0; len(page.Objects) > 0; deleted++ {
		if deleted >= limit {
			return blocked(reasonPurgeIncomplete, fmt.Sprintf("the purge stopped after %d pages and continues on the next pass", limit), pendingRequeue)
		}
		if err := r.S3.DeleteObjects(ctx, r.Endpoint, Region, root, name, page.Objects); err != nil {
			return bucketState{outcome: bucketFailure(err)}
		}
		var err error
		if page, err = r.S3.ListObjectVersions(ctx, r.Endpoint, Region, root, name, "", ""); err != nil {
			return bucketState{outcome: bucketFailure(err)}
		}
	}
	return bucketState{}
}

func (r *BucketReconciler) report(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, state bucketState) ctrl.Result {
	err := updateStatus(ctx, r.Client, bucket, func(latest *v1alpha1.ObjectStorageBucket) {
		latest.Status.ObservedGeneration = latest.Generation
		if state.bucketName != "" {
			latest.Status.BucketName = state.bucketName
			latest.Status.Endpoint = state.endpoint
		}
		if state.seen.usage != nil {
			latest.Status.Usage = state.seen.usage
		}
		v1alpha1.SetCondition(&latest.Status.Conditions, condition(v1alpha1.ConditionReady, state.ready(), state.reason, state.message, latest.Generation))
		if quota := state.seen.quota; quota != nil {
			v1alpha1.SetCondition(&latest.Status.Conditions, condition(conditionQuotaApplied, quota.ok, quota.reason, quota.message, latest.Generation))
		}
	})
	if err != nil {
		logf.FromContext(ctx).Info("status write failed", "error", err.Error())
	}
	return ctrl.Result{RequeueAfter: state.requeue}
}

func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-bucket").
		For(&v1alpha1.ObjectStorageBucket{}).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(r.bucketsOfSetting), builder.WithPredicates(predicate.NewPredicateFuncs(objectStorageSetting))).
		Complete(r)
}
