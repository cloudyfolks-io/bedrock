package objectstorage

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/admin"
)

type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

func quotaReconciler(url string, now *clock) *BucketReconciler {
	r := bucketReconciler(url)
	r.Admin = &admin.Client{Now: now.read}
	r.Now = now.read
	return r
}

func startClock() *clock {
	return &clock{now: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)}
}

func adminReady(t *testing.T) {
	t.Helper()
	if _, _, err := EnsureAdminUser(context.Background(), testClient); err != nil {
		t.Fatal(err)
	}
	makeRookUserReady(t, AdminUserName)
}

func requireQuota(t *testing.T, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	requireCondition(t, readBucket(t, namespace, name).Status.Conditions, "QuotaApplied", status, reason)
}

func TestAdminUserHasTheStatsAndQuotaCapabilities(t *testing.T) {
	fresh(t)
	if _, ready, err := EnsureAdminUser(context.Background(), testClient); err != nil || ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
	rgw := mustRookUser(t, AdminUserName)
	spec := rgw.Object["spec"].(map[string]any)
	capabilities := spec["capabilities"].(map[string]any)
	if spec["store"] != "bedrock-rgw" || capabilities["buckets"] != "read, write" || capabilities["usage"] != "read" || len(capabilities) != 2 {
		t.Fatalf("spec %+v", spec)
	}
	if _, found := spec["accountRef"]; found {
		t.Fatal("the admin user belongs to an account")
	}
	setRookPhase(t, rgw, "Ready")
	if _, ready, _ := EnsureAdminUser(context.Background(), testClient); ready {
		t.Fatal("ready without keys")
	}
	createRookKeys(t, AdminUserName, fakeRookKeys())
	keys, ready, err := EnsureAdminUser(context.Background(), testClient)
	if err != nil || !ready || keys.AccessKey != "AK" || keys.SecretKey != "SK" {
		t.Fatalf("keys %+v ready %v err %v", keys, ready, err)
	}
}

func TestBucketAppliesTheQuotaAfterCreate(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := quotaReconciler(url, startClock())
	accountReady(t, "acme-prod")
	adminReady(t)
	quota := v1alpha1.ObjectStorageBucketQuota{Bytes: ptr.To(resource.MustParse("1Gi")), Objects: ptr.To(int64(500))}
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Quota: quota})
	settle(t, r, "acme-prod", "data")
	got, found := store.quota("acme-prod-data")
	if !found || got.Get("max-size") != "1073741824" || got.Get("max-objects") != "500" || got.Get("enabled") != "true" {
		t.Fatalf("quota %v %v", got, found)
	}
	requireQuota(t, "acme-prod", "data", metav1.ConditionTrue, "Applied")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	if store.countCalls("ADMIN PUT /admin/bucket") != 0 {
		t.Fatal("an unneeded quota call")
	}
}

func TestBucketChangesAndRemovesTheQuota(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := quotaReconciler(url, startClock())
	accountReady(t, "acme-prod")
	adminReady(t)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Quota: v1alpha1.ObjectStorageBucketQuota{Bytes: ptr.To(resource.MustParse("1Gi"))}})
	settle(t, r, "acme-prod", "data")
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.Quota.Bytes = ptr.To(resource.MustParse("2Gi"))
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if got, _ := store.quota("acme-prod-data"); got.Get("max-size") != "2147483648" || got.Get("max-objects") != "-1" {
		t.Fatalf("quota %v", got)
	}
	bucket = readBucket(t, "acme-prod", "data")
	bucket.Spec.Quota = v1alpha1.ObjectStorageBucketQuota{}
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if _, found := store.quota("acme-prod-data"); found {
		t.Fatal("the quota stayed")
	}
	if _, kept := readBucket(t, "acme-prod", "data").Annotations[AppliedQuotaAnnotation]; kept {
		t.Fatal("annotation kept")
	}
	requireQuota(t, "acme-prod", "data", metav1.ConditionTrue, "Applied")
}

func TestBucketWithoutAQuotaMakesNoQuotaCall(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := quotaReconciler(url, startClock())
	accountReady(t, "acme-prod")
	adminReady(t)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	if store.countCalls("ADMIN PUT /admin/bucket") != 0 {
		t.Fatal("a quota call was made")
	}
	requireQuota(t, "acme-prod", "data", metav1.ConditionTrue, "Applied")
}

func TestBucketReportsAPendingAdminUserWithoutLosingReady(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	r := quotaReconciler(url, startClock())
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Quota: v1alpha1.ObjectStorageBucketQuota{Objects: ptr.To(int64(10))}})
	settle(t, r, "acme-prod", "data")
	requireQuota(t, "acme-prod", "data", metav1.ConditionFalse, "AdminPending")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
}

func TestBucketRefusesANegativeQuota(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := quotaReconciler(url, startClock())
	accountReady(t, "acme-prod")
	adminReady(t)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Quota: v1alpha1.ObjectStorageBucketQuota{Bytes: ptr.To(resource.MustParse("-5"))}})
	settle(t, r, "acme-prod", "data")
	requireQuota(t, "acme-prod", "data", metav1.ConditionFalse, "QuotaInvalid")
	if store.countCalls("ADMIN PUT /admin/bucket") != 0 {
		t.Fatal("a quota call was made")
	}
}

func TestBucketReportsAFailedQuotaCall(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := quotaReconciler(url, startClock())
	accountReady(t, "acme-prod")
	adminReady(t)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Quota: v1alpha1.ObjectStorageBucketQuota{Objects: ptr.To(int64(10))}})
	reconcileBucket(t, r, "acme-prod", "data")
	store.mu.Lock()
	store.failAdmin = 403
	store.mu.Unlock()
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.Quota.Objects = ptr.To(int64(20))
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	requireQuota(t, "acme-prod", "data", metav1.ConditionFalse, "QuotaFailed")
}

func TestBucketReportsUsageAndRefreshesEveryFiveMinutes(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	clock := startClock()
	r := quotaReconciler(url, clock)
	accountReady(t, "acme-prod")
	adminReady(t)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	store.stats["acme-prod-data"] = [2]int64{4096, 7}
	settle(t, r, "acme-prod", "data")
	usage := readBucket(t, "acme-prod", "data").Status.Usage
	if usage == nil || usage.Bytes != 4096 || usage.Objects != 7 || !usage.SampledAt.Time.Equal(clock.now) {
		t.Fatalf("usage %+v", usage)
	}
	store.stats["acme-prod-data"] = [2]int64{8192, 9}
	clock.now = clock.now.Add(4 * time.Minute)
	reconcileBucket(t, r, "acme-prod", "data")
	if got := readBucket(t, "acme-prod", "data").Status.Usage; got.Bytes != 4096 {
		t.Fatalf("usage refreshed too early: %+v", got)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	reconcileBucket(t, r, "acme-prod", "data")
	if got := readBucket(t, "acme-prod", "data").Status.Usage; got.Bytes != 8192 || got.Objects != 9 {
		t.Fatalf("usage not refreshed: %+v", got)
	}
}

func TestBucketKeepsTheLastUsageWhenStatsFail(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	clock := startClock()
	r := quotaReconciler(url, clock)
	accountReady(t, "acme-prod")
	adminReady(t)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	store.stats["acme-prod-data"] = [2]int64{100, 1}
	settle(t, r, "acme-prod", "data")
	clock.now = clock.now.Add(time.Hour)
	store.mu.Lock()
	store.failAdmin = 503
	store.mu.Unlock()
	reconcileBucket(t, r, "acme-prod", "data")
	if got := readBucket(t, "acme-prod", "data").Status.Usage; got == nil || got.Bytes != 100 {
		t.Fatalf("usage %+v", got)
	}
}
