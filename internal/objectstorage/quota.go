package objectstorage

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

const (
	AppliedQuotaAnnotation = "bedrock.cloudyfolks.io/applied-quota"
	usageInterval          = 5 * time.Minute
	noQuota                = "0/0"
)

type quotaState struct {
	reason  string
	message string
	ok      bool
}

type observation struct {
	quota *quotaState
	usage *v1alpha1.ObjectStorageBucketUsage
}

func quotaOf(bucket *v1alpha1.ObjectStorageBucket) (bytes, objects int64, err error) {
	quota := bucket.Spec.Quota
	if quota.Bytes != nil {
		bytes = quota.Bytes.Value()
	}
	if quota.Objects != nil {
		objects = *quota.Objects
	}
	if bytes < 0 || objects < 0 {
		return 0, 0, ErrQuotaInvalid
	}
	return bytes, objects, nil
}

func (r *BucketReconciler) observe(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, name string) (observation, error) {
	bytes, objects, err := quotaOf(bucket)
	if err != nil {
		return observation{quota: &quotaState{reason: reasonQuotaInvalid, message: err.Error()}}, nil
	}
	wanted := fmt.Sprintf("%d/%d", bytes, objects)
	applied := bucket.Annotations[AppliedQuotaAnnotation]
	pending := wanted != noQuota && wanted != applied || wanted == noQuota && applied != ""
	keys, ready, err := EnsureAdminUser(ctx, r.Client)
	if err != nil {
		return observation{}, err
	}
	if !ready {
		if pending {
			return observation{quota: &quotaState{reason: "AdminPending", message: "the object store has not made the admin user yet"}}, nil
		}
		return observation{quota: &quotaState{ok: true, reason: "Applied"}}, nil
	}
	quota := r.reconcileQuota(ctx, bucket, keys, name, bytes, objects, wanted, pending)
	return observation{quota: quota, usage: r.sampleUsage(ctx, bucket, keys, name)}, nil
}

func (r *BucketReconciler) reconcileQuota(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, keys s3.Credentials, name string, bytes, objects int64, wanted string, pending bool) *quotaState {
	if !pending {
		return &quotaState{ok: true, reason: "Applied"}
	}
	var callErr error
	target := wanted
	if wanted == noQuota {
		callErr = r.Admin.RemoveBucketQuota(ctx, r.Endpoint, Region, keys, name)
		target = ""
	} else {
		callErr = r.Admin.SetBucketQuota(ctx, r.Endpoint, Region, keys, name, bytes, objects)
	}
	if callErr == nil {
		callErr = r.setAnnotation(ctx, bucket, AppliedQuotaAnnotation, target)
	}
	if callErr != nil {
		return &quotaState{reason: failureReason(callErr, "QuotaFailed"), message: callErr.Error()}
	}
	return &quotaState{ok: true, reason: "Applied"}
}

func (r *BucketReconciler) sampleUsage(ctx context.Context, bucket *v1alpha1.ObjectStorageBucket, keys s3.Credentials, name string) *v1alpha1.ObjectStorageBucketUsage {
	now := r.now()
	if last := bucket.Status.Usage; last != nil && now.Sub(last.SampledAt.Time) < usageInterval {
		return nil
	}
	stats, err := r.Admin.BucketStats(ctx, r.Endpoint, Region, keys, name)
	if err != nil {
		return nil
	}
	return &v1alpha1.ObjectStorageBucketUsage{Bytes: stats.Bytes, Objects: stats.Objects, SampledAt: metav1.NewTime(now)}
}

func (r *BucketReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
