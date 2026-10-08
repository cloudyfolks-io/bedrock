package objectstorage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

type recorded struct{ method, query, body, authorization string }

type fakeStore struct {
	mu    sync.Mutex
	seen  []recorded
	reply func(recorded, http.ResponseWriter)
}

func newFakeStore(t *testing.T, reply func(recorded, http.ResponseWriter)) (*fakeStore, string) {
	t.Helper()
	store := &fakeStore{reply: reply}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := recorded{method: r.Method, query: r.URL.RawQuery, body: string(body), authorization: r.Header.Get("Authorization")}
		store.mu.Lock()
		store.seen = append(store.seen, call)
		store.mu.Unlock()
		store.reply(call, w)
	}))
	t.Cleanup(server.Close)
	return store, server.URL
}

func (f *fakeStore) take() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := f.seen
	f.seen = nil
	return seen
}

func noPolicy(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `<Error><Code>NoSuchBucketPolicy</Code></Error>`)
}

func versioningAnswer(status string) string {
	return `<VersioningConfiguration><Status>` + status + `</Status></VersioningConfiguration>`
}

func bucketReconciler(url string) *BucketReconciler {
	fixed := func() time.Time { return time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC) }
	return &BucketReconciler{Client: testClient, S3: &s3.Client{Now: fixed}, Endpoint: url}
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

func requireBucketReason(t *testing.T, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	requireCondition(t, readBucket(t, namespace, name).Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func readyOwner(t *testing.T, namespace, name string) {
	t.Helper()
	createUser(t, namespace, name, v1alpha1.ObjectStorageUserSpec{})
	user := readUser(t, namespace, name)
	user.Status.CredentialsSecret = CredentialsSecretName(RGWUserName(namespace, name))
	user.Status.Conditions = []metav1.Condition{condition(v1alpha1.ConditionReady, true, "Ready", "", user.Generation)}
	user.Status.Conditions[0].LastTransitionTime = metav1.Now()
	if err := testClient.Status().Update(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	createCredentials(t, namespace, RGWUserName(namespace, name), map[string][]byte{"AccessKey": []byte("AK"), "SecretKey": []byte("SK")})
}

func getClaim(t *testing.T, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	claim, found, err := getRook(context.Background(), testClient, objectBucketClaimGVK, client.ObjectKey{Namespace: namespace, Name: name})
	if err != nil || !found {
		t.Fatalf("claim %v %v", found, err)
	}
	return claim
}

func bindClaim(t *testing.T, namespace, name, bucketName string) {
	t.Helper()
	setRookPhase(t, getClaim(t, namespace, name), "Bound")
	data := map[string]string{"BUCKET_NAME": bucketName, "BUCKET_HOST": "rook-ceph-rgw-bedrock-rgw.rook-ceph.svc", "BUCKET_PORT": "80"}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: data}
	if err := testClient.Create(context.Background(), config); err != nil {
		t.Fatal(err)
	}
}

func boundBucket(t *testing.T, r *BucketReconciler, annotations map[string]string, spec v1alpha1.ObjectStorageBucketSpec) {
	t.Helper()
	createStore(t, "Ready")
	readyOwner(t, "acme-prod", "app")
	bucket := &v1alpha1.ObjectStorageBucket{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "acme-prod", Annotations: annotations}, Spec: spec}
	if err := testClient.Create(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	bindClaim(t, "acme-prod", "data", "data-x1y2z")
}

func requireNoRequests(t *testing.T, store *fakeStore) {
	t.Helper()
	if seen := store.take(); len(seen) != 0 {
		t.Fatalf("requests %+v", seen)
	}
}

func answerEverythingWith(status int) func(recorded, http.ResponseWriter) {
	return func(_ recorded, w http.ResponseWriter) { w.WriteHeader(status) }
}

func TestBucketCreatesTheClaim(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	readyOwner(t, "acme-prod", "app")
	_, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	quota := v1alpha1.ObjectStorageBucketQuota{Bytes: resource.NewQuantity(1000, resource.BinarySI), Objects: int64Ptr(50)}
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Quota: quota})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "ClaimPending")
	claim := getClaim(t, "acme-prod", "data")
	config, _, _ := unstructured.NestedStringMap(claim.Object, "spec", "additionalConfig")
	if config["bucketOwner"] != "acme-prod.app" || config["bucketMaxSize"] != "1000" || config["bucketMaxObjects"] != "50" {
		t.Fatalf("config %+v", config)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	if again := getClaim(t, "acme-prod", "data"); again.GetResourceVersion() != claim.GetResourceVersion() {
		t.Fatal("the claim changed")
	}
}

func TestBucketReportsEndpointAndName(t *testing.T) {
	fresh(t)
	_, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	reconcileBucket(t, r, "acme-prod", "data")
	bucket := readBucket(t, "acme-prod", "data")
	if bucket.Status.BucketName != "data-x1y2z" || bucket.Status.Endpoint != "http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80" {
		t.Fatalf("status %+v", bucket.Status)
	}
	requireCondition(t, bucket.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, "Bound")
	requireCondition(t, bucket.Status.Conditions, "QuotaApplied", metav1.ConditionTrue, "Applied")
}

func TestBucketWaitsForTheUser(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	_, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "UserNotFound")
	createUser(t, "acme-prod", "app", v1alpha1.ObjectStorageUserSpec{})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "UserNotReady")
	if countRook(t, objectBucketClaimGVK, "acme-prod") != 0 {
		t.Fatal("a claim exists")
	}
}

func TestBucketNeverCrossesNamespaces(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	readyOwner(t, "acme-prod", "app")
	_, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	createBucket(t, "beta-prod", "data", v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	reconcileBucket(t, r, "beta-prod", "data")
	requireBucketReason(t, "beta-prod", "data", metav1.ConditionFalse, "UserNotFound")
	if countRook(t, objectBucketClaimGVK, "beta-prod") != 0 {
		t.Fatal("a claim exists")
	}
}

func TestBucketEnablesVersioning(t *testing.T) {
	fresh(t)
	current := ""
	store, url := newFakeStore(t, func(call recorded, w http.ResponseWriter) {
		if call.method == http.MethodGet {
			io.WriteString(w, versioningAnswer(current))
		}
	})
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Versioning: true})
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	seen := store.take()
	if len(seen) != 2 || seen[0].method != "GET" || seen[1].method != "PUT" || seen[1].query != "versioning" || !strings.Contains(seen[1].body, "<Status>Enabled</Status>") {
		t.Fatalf("requests %+v", seen)
	}
	if readBucket(t, "acme-prod", "data").Annotations[AppliedVersioningAnnotation] != "Enabled" {
		t.Fatal("annotation missing")
	}
	current = "Enabled"
	reconcileBucket(t, r, "acme-prod", "data")
	for _, call := range store.take() {
		if call.method == "PUT" {
			t.Fatalf("unneeded put %+v", call)
		}
	}
}

func TestBucketSuspendsVersioning(t *testing.T) {
	fresh(t)
	store, url := newFakeStore(t, func(call recorded, w http.ResponseWriter) {
		if call.method == http.MethodGet {
			io.WriteString(w, versioningAnswer("Enabled"))
		}
	})
	r := bucketReconciler(url)
	boundBucket(t, r, map[string]string{AppliedVersioningAnnotation: "Enabled"}, v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	seen := store.take()
	if len(seen) != 2 || seen[1].method != "PUT" || !strings.Contains(seen[1].body, "<Status>Suspended</Status>") {
		t.Fatalf("requests %+v", seen)
	}
	if readBucket(t, "acme-prod", "data").Annotations[AppliedVersioningAnnotation] != "Suspended" {
		t.Fatal("annotation not suspended")
	}
	reconcileBucket(t, r, "acme-prod", "data")
	requireNoRequests(t, store)
}

func TestBucketSkipsS3WhenNothingToApply(t *testing.T) {
	fresh(t)
	store, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	reconcileBucket(t, r, "acme-prod", "data")
	reconcileBucket(t, r, "acme-prod", "data")
	requireNoRequests(t, store)
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionTrue, "Bound")
}

const examplePolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`

const reorderedPolicy = `{"Statement":[{"Resource":"arn:aws:s3:::b/*","Action":"s3:GetObject","Principal":"*","Effect":"Allow"}],"Version":"2012-10-17"}`

func TestBucketAppliesThePolicy(t *testing.T) {
	fresh(t)
	stored := ""
	store, url := newFakeStore(t, func(call recorded, w http.ResponseWriter) {
		switch {
		case call.method == http.MethodGet && stored == "":
			noPolicy(w)
		case call.method == http.MethodGet:
			io.WriteString(w, stored)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Policy: examplePolicy})
	store.take()
	reconcileBucket(t, r, "acme-prod", "data")
	seen := store.take()
	if len(seen) != 2 || seen[1].method != "PUT" || seen[1].query != "policy" || seen[1].body != examplePolicy {
		t.Fatalf("requests %+v", seen)
	}
	if readBucket(t, "acme-prod", "data").Annotations[AppliedPolicyAnnotation] != "true" {
		t.Fatal("annotation missing")
	}
	stored = reorderedPolicy
	reconcileBucket(t, r, "acme-prod", "data")
	for _, call := range store.take() {
		if call.method == "PUT" {
			t.Fatalf("unneeded put %+v", call)
		}
	}
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.Policy = ""
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	seen = store.take()
	if len(seen) != 2 || seen[1].method != "DELETE" {
		t.Fatalf("requests %+v", seen)
	}
	if _, kept := readBucket(t, "acme-prod", "data").Annotations[AppliedPolicyAnnotation]; kept {
		t.Fatal("annotation kept")
	}
	reconcileBucket(t, r, "acme-prod", "data")
	requireNoRequests(t, store)
}

func TestBucketRejectsAnInvalidPolicy(t *testing.T) {
	fresh(t)
	store, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Policy: "{}"})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "PolicyInvalid")
	requireNoRequests(t, store)
}

func TestBucketReportsPolicyRejectedByTheStore(t *testing.T) {
	fresh(t)
	_, url := newFakeStore(t, func(call recorded, w http.ResponseWriter) {
		if call.method == http.MethodGet {
			noPolicy(w)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `<Error><Code>MalformedPolicy</Code><Message>key AK secret SK</Message></Error>`)
	})
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Policy: examplePolicy})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "PolicyRejected")
	message := readBucket(t, "acme-prod", "data").Status.Conditions[0].Message
	if !strings.Contains(message, "MalformedPolicy") || strings.Contains(message, "AK") || strings.Contains(message, "SK") {
		t.Fatalf("message %q", message)
	}
}

func TestBucketUsesOnlyTheOwnersCredentials(t *testing.T) {
	fresh(t)
	store, url := newFakeStore(t, func(call recorded, w http.ResponseWriter) {
		if call.method == http.MethodGet {
			noPolicy(w)
		}
	})
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Policy: examplePolicy, Versioning: true})
	reconcileBucket(t, r, "acme-prod", "data")
	seen := store.take()
	if len(seen) == 0 {
		t.Fatal("no requests")
	}
	for _, call := range seen {
		if !strings.Contains(call.authorization, "Credential=AK/") {
			t.Fatalf("authorization %q", call.authorization)
		}
	}
}

func TestBucketOwnerChangeIsRefused(t *testing.T) {
	fresh(t)
	createStore(t, "Ready")
	readyOwner(t, "acme-prod", "app")
	readyOwner(t, "acme-prod", "other")
	_, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	createBucket(t, "acme-prod", "data", v1alpha1.ObjectStorageBucketSpec{UserRef: "app"})
	reconcileBucket(t, r, "acme-prod", "data")
	before := getClaim(t, "acme-prod", "data").GetResourceVersion()
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.UserRef = "other"
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "OwnerChangeNotSupported")
	if getClaim(t, "acme-prod", "data").GetResourceVersion() != before {
		t.Fatal("the claim changed")
	}
}

func TestBucketQuotaChangeIsReported(t *testing.T) {
	fresh(t)
	_, url := newFakeStore(t, answerEverythingWith(500))
	r := bucketReconciler(url)
	quota := v1alpha1.ObjectStorageBucketQuota{Bytes: resource.NewQuantity(1000, resource.BinarySI)}
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Quota: quota})
	bucket := readBucket(t, "acme-prod", "data")
	bucket.Spec.Quota.Bytes = resource.NewQuantity(2000, resource.BinarySI)
	if err := testClient.Update(context.Background(), bucket); err != nil {
		t.Fatal(err)
	}
	reconcileBucket(t, r, "acme-prod", "data")
	requireCondition(t, readBucket(t, "acme-prod", "data").Status.Conditions, "QuotaApplied", metav1.ConditionFalse, "ChangeNotSupported")
	size, _, _ := unstructured.NestedString(getClaim(t, "acme-prod", "data").Object, "spec", "additionalConfig", "bucketMaxSize")
	if size != "1000" {
		t.Fatalf("bucketMaxSize %q", size)
	}
}

func TestBucketReportsAnUnavailableStoreAsNotReady(t *testing.T) {
	fresh(t)
	store, url := newFakeStore(t, answerEverythingWith(503))
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Versioning: true})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "StoreNotReady")
	if len(store.take()) == 0 {
		t.Fatal("the store was not called")
	}
}

func TestBucketReportsAnUnreachableStoreAsNotReady(t *testing.T) {
	fresh(t)
	_, url := newFakeStore(t, answerEverythingWith(200))
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Policy: examplePolicy})
	r.Endpoint = "http://127.0.0.1:1"
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "StoreNotReady")
}

func TestBucketKeepsClientErrorsAsRefusals(t *testing.T) {
	fresh(t)
	_, url := newFakeStore(t, answerEverythingWith(403))
	r := bucketReconciler(url)
	boundBucket(t, r, nil, v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Versioning: true})
	reconcileBucket(t, r, "acme-prod", "data")
	requireBucketReason(t, "acme-prod", "data", metav1.ConditionFalse, "VersioningFailed")
}
