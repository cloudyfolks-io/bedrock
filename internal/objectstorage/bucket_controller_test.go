package objectstorage

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func rulesOf(rules Rules) func(context.Context) (Rules, error) {
	return func(context.Context) (Rules, error) { return rules, nil }
}

func bucketReconcilerWith(url string, rules Rules) *BucketReconciler {
	return &BucketReconciler{Client: testClient, S3: fixedClient(), Endpoint: url, Rules: rulesOf(rules)}
}

func bucketReconciler(url string) *BucketReconciler {
	return bucketReconcilerWith(url, DefaultRules())
}

func reconcileBucket(t *testing.T, r *BucketReconciler, namespace, name string) {
	t.Helper()
	request := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func readBucket(t *testing.T, namespace, name string) *v1alpha1.ObjectStorageBucket {
	t.Helper()
	var bucket v1alpha1.ObjectStorageBucket
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &bucket); err != nil {
		t.Fatal(err)
	}
	return &bucket
}

func bucketGone(t *testing.T, namespace, name string) bool {
	t.Helper()
	var bucket v1alpha1.ObjectStorageBucket
	err := testClient.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &bucket)
	return apierrors.IsNotFound(err)
}

func requireBucketReason(t *testing.T, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	requireCondition(t, readBucket(t, namespace, name).Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func accountReady(t *testing.T, namespace string) {
	t.Helper()
	createStore(t, "Ready")
	readyAccountOf(t, namespace)
}

func settle(t *testing.T, r *BucketReconciler, namespace, name string) {
	t.Helper()
	reconcileBucket(t, r, namespace, name)
	reconcileBucket(t, r, namespace, name)
}

func callsWithout(calls []string, ignored ...string) []string {
	kept := []string{}
	for _, call := range calls {
		skip := false
		for _, prefix := range ignored {
			skip = skip || strings.HasPrefix(call, prefix)
		}
		if !skip {
			kept = append(kept, call)
		}
	}
	return kept
}

func TestBucketMakesTheBucketWithAGeneratedName(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	if !store.has("acme-prod-data") {
		t.Fatal("the bucket was not created")
	}
	bucket := readBucket(t, "acme-prod", "data")
	if bucket.Status.BucketName != "acme-prod-data" || bucket.Status.Endpoint != url {
		t.Fatalf("status %+v", bucket.Status)
	}
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
	if len(bucket.Finalizers) != 1 || bucket.Finalizers[0] != bucketFinalizer {
		t.Fatalf("finalizers %v", bucket.Finalizers)
	}
	if countRook(t, objectBucketClaimGVK, "acme-prod") != 0 {
		t.Fatal("a claim exists")
	}
	for _, authorization := range store.credentials {
		if !strings.Contains(authorization, "Credential=ROOTAK/") {
			t.Fatalf("authorization %q", authorization)
		}
	}
}

func TestBucketUsesTheChosenName(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets"})
	settle(t, r, "acme-prod", "data")
	if !store.has("acme-assets") || store.has("acme-prod-data") {
		t.Fatal("the chosen name was not used")
	}
	if readBucket(t, "acme-prod", "data").Status.BucketName != "acme-assets" {
		t.Fatal("status name")
	}
}

func TestBucketAdoptsABucketTheAccountOwns(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.fill("acme-assets", 3)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets"})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
	if len(store.objects["acme-assets"]) != 3 {
		t.Fatal("the adopted bucket lost objects")
	}
}

func TestBucketAdoptsWhenTheCreateSaysOwnedByYou(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.hideFromHead["acme-prod-data"] = true
	store.buckets["acme-prod-data"] = true
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
}

func TestBucketNameTakenWhenTheCreateRaces(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.hideFromHead["acme-assets"] = true
	store.foreign["acme-assets"] = true
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets"})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "NameTaken")
}

func TestBucketNameTakenByAnotherAccount(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.foreign["acme-assets"] = true
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets"})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "NameTaken")
	if readBucket(t, "acme-prod", "data").Status.BucketName != "" {
		t.Fatal("a refused bucket reports a name")
	}
	if store.has("acme-assets") {
		t.Fatal("the foreign bucket changed")
	}
}

func TestBucketNameTakenInTheSameProject(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "first", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets"})
	createBucket(t, "acme-prod", "second", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets"})
	settle(t, r, "acme-prod", "first")
	settle(t, r, "acme-prod", "second")
	requireBucketReason(t, "acme-prod", "first", metav1.ConditionTrue, "Bound")
	requireBucketReason(t, "acme-prod", "second", metav1.ConditionFalse, "NameTaken")
	if !store.has("acme-assets") {
		t.Fatal("the bucket is gone")
	}
}

func TestBucketSameObjectNameInTwoProjects(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	readyAccountOf(t, "beta-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	createBucket(t, "beta-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	settle(t, r, "beta-prod", "data")
	if !store.has("acme-prod-data") || !store.has("beta-prod-data") {
		t.Fatal("both buckets must exist")
	}
}

func TestBucketNameProblems(t *testing.T) {
	rules := Rules{ReservedNames: []string{"admin"}, ReservedPrefixes: []string{"git-", "e2e-"}}
	cases := []struct {
		name     string
		approved bool
		want     string
	}{
		{"acme-data", false, ""},
		{"ab", false, reasonNameInvalid},
		{"Upper-case", false, reasonNameInvalid},
		{"-leading", false, reasonNameInvalid},
		{"trailing-", false, reasonNameInvalid},
		{"under_score", false, reasonNameInvalid},
		{"192.168.0.1", false, reasonNameInvalid},
		{strings.Repeat("a", 64), false, reasonNameInvalid},
		{"admin", false, reasonNameReserved},
		{"git-repos", false, reasonNameReserved},
		{"e2e-run", false, reasonNameReserved},
		{"admin", true, ""},
		{"git-repos", true, ""},
		{"gitless", false, ""},
	}
	for _, tc := range cases {
		if got := nameProblem(tc.name, rules, tc.approved); got != tc.want {
			t.Fatalf("%q approved %v: got %q want %q", tc.name, tc.approved, got, tc.want)
		}
	}
}

func TestBucketRefusesAReservedName(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconcilerWith(url, Rules{ReservedPrefixes: []string{"git-"}, MaxBuckets: 100})
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "repos", v1alpha1.ObjectStorageBucketSpec{BucketName: "git-repos"})
	settle(t, r, "acme-prod", "repos")
	requireBucketReason(t, "acme-prod", "repos", metav1.ConditionFalse, "NameReserved")
	if len(store.take()) != 0 {
		t.Fatal("the store was called")
	}
	bucket := readBucket(t, "acme-prod", "repos")
	bucket.Annotations = map[string]string{ReservedNameApprovedAnnotation: "true"}
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	settle(t, r, "acme-prod", "repos")
	requireBucketReason(t, "acme-prod", "repos", metav1.ConditionTrue, "Bound")
}

func TestBucketRefusesAReservedGeneratedName(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	r := bucketReconcilerWith(url, Rules{ReservedNames: []string{"acme-prod-data"}, MaxBuckets: 100})
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "NameReserved")
}

func TestBucketLimitRefusesOnlyNewBuckets(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	accountReady(t, "acme-prod")
	generous := bucketReconcilerWith(url, Rules{MaxBuckets: 3})
	for _, name := range []string{"one", "two"} {
		createBucket(t, "acme-prod", name, v1alpha1.ObjectStorageBucketSpec{})
		settle(t, generous, "acme-prod", name)
	}
	strict := bucketReconcilerWith(url, Rules{MaxBuckets: 2})
	createBucket(t, "acme-prod", "three", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, strict, "acme-prod", "three")
	requireBucketReason(t, "acme-prod", "three", metav1.ConditionFalse, "BucketLimit")
	if store.has("acme-prod-three") {
		t.Fatal("a bucket over the limit was created")
	}
	lower := bucketReconcilerWith(url, Rules{MaxBuckets: 1})
	reconcileBucket(t, lower, "acme-prod", "one")
	reconcileBucket(t, lower, "acme-prod", "two")
	requireBucketReason(t, "acme-prod", "one", metav1.ConditionTrue, "Bound")
	requireBucketReason(t, "acme-prod", "two", metav1.ConditionTrue, "Bound")
}

func TestBucketLimitIgnoresDeletingBuckets(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	accountReady(t, "acme-prod")
	r := bucketReconcilerWith(url, Rules{MaxBuckets: 1})
	createBucket(t, "acme-prod", "one", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "one")
	store.fill("acme-prod-one", 1)
	if err := testClient.Delete(context.Background(), readBucket(t, "acme-prod", "one")); err != nil {
		t.Fatal(err)
	}
	createBucket(t, "acme-prod", "two", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "two")
	requireBucketReason(t, "acme-prod", "two", metav1.ConditionTrue, "Bound")
}

func TestBucketWaitsForTheAccount(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	createStore(t, "Ready")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "AccountPending")
	if len(store.take()) != 0 {
		t.Fatal("the store was called")
	}
}

func TestBucketWaitsForTheStore(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	r := bucketReconciler(url)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "StoreNotInstalled")
}

func TestBucketOutsideAProjectNamespace(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	r := bucketReconciler(url)
	createBucket(t, unlabeledNamespace, "data", v1alpha1.ObjectStorageBucketSpec{})
	reconcileBucket(t, r, unlabeledNamespace, "data")
	requireBucketReason(t, unlabeledNamespace, "data", metav1.ConditionFalse, "NamespaceNotProject")
}

func TestBucketMergesTheCorsOrigins(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconcilerWith(url, Rules{CORSOrigins: []string{"https://panel.test"}, MaxBuckets: 100})
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{CORSOrigins: []string{"https://app.test", "https://panel.test"}})
	settle(t, r, "acme-prod", "data")
	got := store.cors["acme-prod-data"]
	if len(got) != 2 || got[0] != "https://panel.test" || got[1] != "https://app.test" {
		t.Fatalf("origins %v", got)
	}
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	for _, call := range store.take() {
		if call == "PUT cors" {
			t.Fatal("an unneeded CORS put")
		}
	}
}

func TestBucketAppliesTheSettingOriginsAlone(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconcilerWith(url, Rules{CORSOrigins: []string{"https://panel.test"}, MaxBuckets: 100})
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	if got := store.cors["acme-prod-data"]; len(got) != 1 || got[0] != "https://panel.test" {
		t.Fatalf("origins %v", got)
	}
}

func TestBucketRemovesCorsWhenNoOriginRemains(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{CORSOrigins: []string{"https://app.test"}})
	settle(t, r, "acme-prod", "data")
	if len(store.cors["acme-prod-data"]) != 1 {
		t.Fatal("origin missing")
	}
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.CORSOrigins = nil
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if _, found := store.cors["acme-prod-data"]; found {
		t.Fatal("the CORS configuration stayed")
	}
}

func TestBucketNeverTouchesCorsWhenThereIsNone(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	for _, call := range store.take() {
		if strings.Contains(call, "cors") {
			t.Fatalf("call %q", call)
		}
	}
}

func TestBucketEnablesAndSuspendsVersioning(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Versioning: true})
	settle(t, r, "acme-prod", "data")
	if store.versioning["acme-prod-data"] != "Enabled" {
		t.Fatalf("versioning %q", store.versioning["acme-prod-data"])
	}
	if readBucket(t, "acme-prod", "data").Annotations[AppliedVersioningAnnotation] != "Enabled" {
		t.Fatal("annotation missing")
	}
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	for _, call := range store.take() {
		if call == "PUT versioning" {
			t.Fatal("an unneeded versioning put")
		}
	}
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.Versioning = false
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if store.versioning["acme-prod-data"] != "Suspended" {
		t.Fatalf("versioning %q", store.versioning["acme-prod-data"])
	}
}

const examplePolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`

const reorderedPolicy = `{"Statement":[{"Resource":"arn:aws:s3:::b/*","Action":"s3:GetObject","Principal":"*","Effect":"Allow"}],"Version":"2012-10-17"}`

func TestBucketAppliesAndRemovesThePolicy(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Policy: examplePolicy})
	settle(t, r, "acme-prod", "data")
	if store.policies["acme-prod-data"] != examplePolicy {
		t.Fatalf("policy %q", store.policies["acme-prod-data"])
	}
	store.policies["acme-prod-data"] = reorderedPolicy
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	for _, call := range store.take() {
		if call == "PUT policy" {
			t.Fatal("an unneeded policy put")
		}
	}
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.Policy = ""
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if _, found := store.policies["acme-prod-data"]; found {
		t.Fatal("the policy stayed")
	}
}

func TestBucketRejectsAnInvalidPolicy(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Policy: "{}"})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "PolicyInvalid")
	if _, found := store.policies["acme-prod-data"]; found {
		t.Fatal("an invalid policy was stored")
	}
}

func TestBucketReportsAnUnavailableStoreAsNotReady(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.failWith = http.StatusServiceUnavailable
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "StoreNotReady")
}

func TestBucketReportsAnUnreachableStoreAsNotReady(t *testing.T) {
	fresh(t)
	r := bucketReconciler("http://127.0.0.1:1")
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "StoreNotReady")
}

func TestBucketReportsADeniedCreateAsAFailure(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.failWith = http.StatusForbidden
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "BucketFailed")
}

func TestBucketWithAnOldClaimIsLegacy(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "objectbucket.io/v1alpha1",
		"kind":       "ObjectBucketClaim",
		"metadata":   map[string]any{"name": "data", "namespace": "acme-prod"},
		"spec":       map[string]any{"generateBucketName": "data", "storageClassName": "bucket"},
	}}
	if err := testClient.Create(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "LegacyBucket")
	if len(store.take()) != 0 {
		t.Fatal("the store was called for a legacy bucket")
	}
	if err := testClient.Delete(context.Background(), readBucket(t, "acme-prod", "data")); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if !bucketGone(t, "acme-prod", "data") || len(store.take()) != 0 {
		t.Fatal("a legacy bucket delete touched the store")
	}
	if countRook(t, objectBucketClaimGVK, "acme-prod") != 1 {
		t.Fatal("the legacy claim was deleted")
	}
}

func deleteBucketObject(t *testing.T, namespace, name string) {
	t.Helper()
	if err := testClient.Delete(context.Background(), readBucket(t, namespace, name)); err != nil {
		t.Fatal(err)
	}
}

func TestBucketDeleteOfAnEmptyBucket(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	deleteBucketObject(t, "acme-prod", "data")
	reconcileBucket(t, r, "acme-prod", "data")
	if store.has("acme-prod-data") || !bucketGone(t, "acme-prod", "data") {
		t.Fatal("the bucket or its object remains")
	}
}

func TestBucketDeleteWaitsWhenNotEmpty(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "acme-prod", "data")
	store.fill("acme-prod-data", 5)
	deleteBucketObject(t, "acme-prod", "data")
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "NotEmpty")
	if !strings.Contains(readBucket(t, "acme-prod", "data").Status.Conditions[0].Message, "5") {
		t.Fatalf("message %q", readBucket(t, "acme-prod", "data").Status.Conditions[0].Message)
	}
	for _, call := range store.take() {
		if call == "POST delete" || call == "DELETE" {
			t.Fatalf("call %q", call)
		}
	}
	if len(store.objects["acme-prod-data"]) != 5 {
		t.Fatal("objects were removed")
	}
}

func TestBucketForceDestroyPurgesThreePages(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{ForceDestroy: true})
	settle(t, r, "acme-prod", "data")
	store.fill("acme-prod-data", 2500)
	deleteBucketObject(t, "acme-prod", "data")
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	purges := 0
	for _, call := range store.take() {
		if call == "POST delete" {
			purges++
		}
	}
	if purges != 3 || store.has("acme-prod-data") || !bucketGone(t, "acme-prod", "data") {
		t.Fatalf("purges %d, bucket present %v", purges, store.has("acme-prod-data"))
	}
}

func TestBucketPurgeStopsAndReportsIncomplete(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	r.MaxPurgePages = 2
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{ForceDestroy: true})
	settle(t, r, "acme-prod", "data")
	store.fill("acme-prod-data", 2500)
	deleteBucketObject(t, "acme-prod", "data")
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "PurgeIncomplete")
	if len(store.objects["acme-prod-data"]) != 500 {
		t.Fatalf("objects left %d", len(store.objects["acme-prod-data"]))
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if !bucketGone(t, "acme-prod", "data") {
		t.Fatal("the next pass did not finish")
	}
}

func TestBucketInADeletingNamespaceActsAsForceDestroy(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: leavingNamespace}}
	accountReady(t, "leaving-prod")
	createBucket(t, "leaving-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	settle(t, r, "leaving-prod", "data")
	store.fill("leaving-prod-data", 10)
	if err := testClient.Delete(context.Background(), namespace); err != nil {
		t.Fatal(err)
	}
	deleteBucketObject(t, "leaving-prod", "data")
	reconcileBucket(t, r, "leaving-prod", "data")
	if store.has("leaving-prod-data") || !bucketGone(t, "leaving-prod", "data") {
		t.Fatal("the bucket of a deleting namespace was kept")
	}
}

func TestBucketDeleteWithoutAnAccountFinishes(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	createStore(t, "Ready")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	reconcileBucket(t, r, "acme-prod", "data")
	deleteBucketObject(t, "acme-prod", "data")
	reconcileBucket(t, r, "acme-prod", "data")
	if !bucketGone(t, "acme-prod", "data") || len(store.take()) != 0 {
		t.Fatal("the delete needed an account that does not exist")
	}
}

func TestBucketStatusIsWrittenByTheAreaManager(t *testing.T) {
	fresh(t)
	_, url := newFakeS3(t)
	r := bucketReconciler(url)
	createStore(t, "Ready")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{})
	reconcileBucket(t, r, "acme-prod", "data")
	for _, entry := range readBucket(t, "acme-prod", "data").ManagedFields {
		if entry.Subresource == "status" && entry.Manager == v1alpha1.ObjectStorageFieldManager {
			return
		}
	}
	t.Fatalf("managed fields %+v", readBucket(t, "acme-prod", "data").ManagedFields)
}

func TestBucketProvesOwnershipWithTheCreateAnswer(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	store.foreign["acme-assets"] = true
	store.publicHead["acme-assets"] = true
	store.fill("acme-assets", 4)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{BucketName: "acme-assets", ForceDestroy: true})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "NameTaken")
	if readBucket(t, "acme-prod", "data").Status.BucketName != "" {
		t.Fatal("a foreign bucket is reported as ours")
	}
	deleteBucketObject(t, "acme-prod", "data")
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	if !bucketGone(t, "acme-prod", "data") {
		t.Fatal("the finalizer stayed")
	}
	for _, call := range store.take() {
		if call != "" {
			t.Fatalf("the delete of a refused bucket called the store: %q", call)
		}
	}
	if !store.has("acme-assets") || len(store.objects["acme-assets"]) != 4 {
		t.Fatal("a bucket of another account was touched")
	}
}

func TestBucketDeletesAnOwnedBucketWhoseSettingsFailed(t *testing.T) {
	fresh(t)
	store, url := newFakeS3(t)
	r := bucketReconciler(url)
	accountReady(t, "acme-prod")
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{Policy: "{}", ForceDestroy: true})
	settle(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "PolicyInvalid")
	if readBucket(t, "acme-prod", "data").Status.BucketName != "" {
		t.Fatal("the name is reported before the settings are applied")
	}
	store.fill("acme-prod-data", 3)
	deleteBucketObject(t, "acme-prod", "data")
	reconcileBucket(t, r, "acme-prod", "data")
	if store.has("acme-prod-data") || !bucketGone(t, "acme-prod", "data") {
		t.Fatal("an owned bucket was left behind")
	}
}
