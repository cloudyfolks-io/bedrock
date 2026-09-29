package housekeeping

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
)

type hookClient struct {
	client.Client
	afterList func()
}

func (h *hookClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := h.Client.List(ctx, list, opts...)
	if hook := h.afterList; hook != nil {
		h.afterList = nil
		hook()
	}
	return err
}

type failingListClient struct {
	client.Client
	failListType string
}

func (f *failingListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if fmt.Sprintf("%T", list) == f.failListType {
		return errors.New("housekeeping: injected list failure")
	}
	return f.Client.List(ctx, list, opts...)
}

func authCode(name string, expiresAt metav1.Time) *v1alpha1.AuthCode {
	return &v1alpha1.AuthCode{ObjectMeta: objectMeta(name), Spec: v1alpha1.AuthCodeSpec{AuthRequest: "live-request", ExpiresAt: expiresAt}}
}

func refreshToken(name string, expiresAt metav1.Time) *v1alpha1.RefreshToken {
	return &v1alpha1.RefreshToken{ObjectMeta: objectMeta(name), Spec: v1alpha1.RefreshTokenSpec{Family: "family", UserRef: "alice", ClientID: "bedrock-cli", AuthTime: metav1.NewTime(time.Now()), ExpiresAt: expiresAt}}
}

func session(name string, expiresAt metav1.Time) *v1alpha1.Session {
	return &v1alpha1.Session{ObjectMeta: objectMeta(name), Spec: v1alpha1.SessionSpec{UserRef: "alice", AuthTime: metav1.NewTime(time.Now()), ExpiresAt: expiresAt}}
}

func deviceRequest(name string, expiresAt metav1.Time) *v1alpha1.DeviceRequest {
	return &v1alpha1.DeviceRequest{ObjectMeta: objectMeta(name), Spec: v1alpha1.DeviceRequestSpec{ClientID: "bedrock-cli", UserCodeHash: "hash", ExpiresAt: expiresAt}}
}

func apiToken(name string, expiresAt *metav1.Time) *v1alpha1.APIToken {
	return &v1alpha1.APIToken{ObjectMeta: objectMeta(name), Spec: v1alpha1.APITokenSpec{UserRef: "alice", Scopes: []string{"kubernetes"}, ExpiresAt: expiresAt}}
}

func credential(name, method string) *v1alpha1.Credential {
	return &v1alpha1.Credential{ObjectMeta: objectMeta(name), Spec: v1alpha1.CredentialSpec{UserRef: "alice", Method: method, SecretRef: name}}
}

func present(t *testing.T, c client.Client, obj client.Object) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object))
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestSweepDeletesOnlyExpired(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Second)
	sweepAt := start.Add(2 * time.Hour)
	expired := metav1.NewTime(start.Add(time.Hour))
	live := metav1.NewTime(start.Add(3 * time.Hour))
	gone := []client.Object{
		authRequest("expired-request", expired),
		authCode("expired-code", expired),
		refreshToken("expired-refresh", expired),
		session("expired-session", expired),
		deviceRequest("expired-device", expired),
		apiToken("expired-token", &expired),
		credential("pending-totp", v1alpha1.MethodTOTP),
	}
	enrolled := credential("enrolled-totp", v1alpha1.MethodTOTP)
	kept := []client.Object{
		authRequest("live-request", live),
		authCode("live-code", live),
		refreshToken("live-refresh", live),
		session("live-session", live),
		deviceRequest("live-device", live),
		apiToken("live-token", &live),
		apiToken("forever-token", nil),
		enrolled,
		credential("alice-password", v1alpha1.MethodPassword),
	}
	create(t, c, append(append([]client.Object{}, gone...), kept...)...)
	enrolled.Status.EnrolledAt = &metav1.Time{Time: start}
	if err := c.Status().Update(ctx, enrolled); err != nil {
		t.Fatal(err)
	}
	oldKey, err := keys.Generate(rand.Reader, sweepAt.Add(-61*day))
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := keys.Generate(rand.Reader, sweepAt.Add(-day))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []keys.Key{oldKey, newKey} {
		if err := keys.Save(ctx, c, key); err != nil {
			t.Fatal(err)
		}
	}

	swept, err := Sweep(ctx, c, sweepAt)
	if err != nil {
		t.Fatal(err)
	}
	want := Swept{AuthRequests: 1, AuthCodes: 1, RefreshTokens: 1, Sessions: 1, DeviceRequests: 1, APITokens: 1, SigningKeys: 1, PendingCredentials: 1}
	if swept != want {
		t.Fatalf("swept %+v, want %+v", swept, want)
	}
	for _, obj := range gone {
		if present(t, c, obj) {
			t.Errorf("%T %s must be swept", obj, obj.GetName())
		}
	}
	for _, obj := range kept {
		if !present(t, c, obj) {
			t.Errorf("%T %s must stay", obj, obj.GetName())
		}
	}
	loaded, err := keys.Load(ctx, c)
	if err != nil || len(loaded) != 1 || loaded[0].ID != newKey.ID {
		t.Fatalf("keys after the sweep %v, err %v", loaded, err)
	}
	again, err := Sweep(ctx, c, sweepAt)
	if err != nil || again != (Swept{}) {
		t.Fatalf("a second sweep must find nothing, got %+v, err %v", again, err)
	}
}

func TestSweepUsesLatestEnrollmentWrite(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	start := time.Now().UTC()

	protected := credential("recent-reenroll", v1alpha1.MethodTOTP)
	create(t, c, protected)
	if err := c.Get(ctx, client.ObjectKeyFromObject(protected), protected); err != nil {
		t.Fatal(err)
	}
	protected.Annotations = map[string]string{v1alpha1.AnnotationEnrollWriteAt: start.Add(59 * time.Minute).Format(time.RFC3339Nano)}
	if err := c.Update(ctx, protected, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		t.Fatal(err)
	}

	sweepAt := start.Add(65 * time.Minute)
	swept, err := Sweep(ctx, c, sweepAt)
	if err != nil {
		t.Fatal(err)
	}
	if swept.PendingCredentials != 0 {
		t.Fatalf("a credential re-enrolled 59 minutes after its creation must not be swept 6 minutes later, got %+v", swept)
	}
	if !present(t, c, protected) {
		t.Fatal("a credential re-enrolled 59 minutes after its creation must stay, despite an hour-old creationTimestamp")
	}
}

func TestSweepDeleteConflictIsNotCounted(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expired := metav1.NewTime(now.Add(-time.Minute))
	req := authRequest("recreated-request", expired)
	create(t, c, req)

	hook := &hookClient{Client: c}
	hook.afterList = func() {
		var live v1alpha1.AuthRequest
		if err := c.Get(ctx, objectKey(req.Name), &live); err != nil {
			t.Fatal(err)
		}
		uid := live.GetUID()
		if err := c.Delete(ctx, &live, client.Preconditions{UID: &uid}); err != nil {
			t.Fatal(err)
		}
		create(t, c, authRequest(req.Name, expired))
	}

	swept, err := Sweep(ctx, hook, now)
	if err != nil {
		t.Fatal(err)
	}
	if swept.AuthRequests != 0 {
		t.Fatalf("a request recreated between the list and the delete must not be counted, got %+v", swept)
	}
	if !present(t, c, req) {
		t.Fatal("the recreated request must survive the conflicting delete")
	}
}

func TestSweepContinuesPastAFailingKind(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	expired := metav1.NewTime(now.Add(-time.Minute))
	create(t, c,
		authRequest("expired-request", expired),
		authCode("expired-code", expired),
		session("expired-session", expired),
	)
	failing := &failingListClient{Client: c, failListType: "*v1alpha1.AuthCodeList"}

	swept, err := Sweep(ctx, failing, now)
	if err == nil {
		t.Fatal("Sweep must return the injected list error")
	}
	if swept.AuthRequests != 1 {
		t.Fatalf("a kind listed before the failing one must still be swept, got %+v", swept)
	}
	if swept.Sessions != 1 {
		t.Fatalf("a kind listed after the failing one must still be swept, got %+v", swept)
	}
	if swept.AuthCodes != 0 {
		t.Fatalf("the failing kind must report zero, got %+v", swept)
	}
	if !present(t, c, authCode("expired-code", expired)) {
		t.Fatal("the failing kind's object must survive since its list never ran")
	}
}

func TestSweepKeepsAYoungOrphanSigningKey(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	orphan := &v1alpha1.SigningKey{
		ObjectMeta: objectMeta("k-20260928t120000z"),
		Spec:       v1alpha1.SigningKeySpec{Algorithm: v1alpha1.AlgorithmES256, SecretRef: "k-20260928t120000z", NotBefore: metav1.NewTime(now), RetireAfter: metav1.NewTime(now.Add(keys.Lifetime))},
	}
	create(t, c, orphan)

	swept, err := Sweep(ctx, c, now.Add(9*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if swept.SigningKeys != 0 {
		t.Fatalf("an orphan younger than 10 minutes must be kept, got %+v", swept)
	}
	if !present(t, c, orphan) {
		t.Fatal("a young orphan SigningKey must stay")
	}
}

func TestSweepDeletesAnOldOrphanSigningKey(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	orphan := &v1alpha1.SigningKey{
		ObjectMeta: objectMeta("k-20260928t120000z"),
		Spec:       v1alpha1.SigningKeySpec{Algorithm: v1alpha1.AlgorithmES256, SecretRef: "k-20260928t120000z", NotBefore: metav1.NewTime(now), RetireAfter: metav1.NewTime(now.Add(keys.Lifetime))},
	}
	create(t, c, orphan)

	swept, err := Sweep(ctx, c, now.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if swept.SigningKeys != 1 {
		t.Fatalf("an orphan older than 10 minutes must be deleted, got %+v", swept)
	}
	if present(t, c, orphan) {
		t.Fatal("an old orphan SigningKey must be swept")
	}
}

func TestSweepDeletesAnOldSigningKeyWithAnUndecodableSecret(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	key, err := keys.Generate(rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(ctx, c, key); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := c.Get(ctx, objectKey(key.ID), &sec); err != nil {
		t.Fatal(err)
	}
	sec.Data["key.pem"] = []byte("not a pem block")
	if err := c.Update(ctx, &sec); err != nil {
		t.Fatal(err)
	}

	swept, err := Sweep(ctx, c, now.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if swept.SigningKeys != 1 {
		t.Fatalf("a signing key with an undecodable secret older than 10 minutes must be deleted, got %+v", swept)
	}
	var gone v1alpha1.SigningKey
	if err := c.Get(ctx, objectKey(key.ID), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the SigningKey must be deleted, err %v", err)
	}
}

func TestRotateCreatesFirstKeyNow(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	if err := Rotate(ctx, c, rand.Reader, t0.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	loaded, err := keys.Load(ctx, c)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("keys %v, err %v", loaded, err)
	}
	if loaded[0].ID != "k-20260928t120000z" || !loaded[0].NotBefore.Equal(t0) || !loaded[0].RetireAfter.Equal(t0.Add(keys.Lifetime)) {
		t.Fatalf("first key %s %v %v", loaded[0].ID, loaded[0].NotBefore, loaded[0].RetireAfter)
	}
	if signing, ok := keys.Signing(loaded, t0); !ok || signing.ID != loaded[0].ID {
		t.Fatal("the first key must sign at once")
	}
}

func TestRotateCreatesNextKeyADayEarly(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	if err := Rotate(ctx, c, rand.Reader, t0); err != nil {
		t.Fatal(err)
	}
	if err := Rotate(ctx, c, rand.Reader, t0.Add(28*day)); err != nil {
		t.Fatal(err)
	}
	if loaded, _ := keys.Load(ctx, c); len(loaded) != 1 {
		t.Fatalf("no successor before a day ahead, got %d keys", len(loaded))
	}
	if err := Rotate(ctx, c, rand.Reader, t0.Add(29*day)); err != nil {
		t.Fatal(err)
	}
	loaded, err := keys.Load(ctx, c)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("keys %v, err %v", loaded, err)
	}
	published := keys.Published(loaded, t0.Add(29*day))
	if len(published) != 2 || !published[1].NotBefore.Equal(t0.Add(keys.Lifetime)) {
		t.Fatalf("the successor must be published a day ahead, got %v", published)
	}
	if signing, ok := keys.Signing(loaded, t0.Add(29*day)); !ok || signing.ID != "k-20260928t120000z" {
		t.Fatalf("the old key signs until it retires, got %s", signing.ID)
	}
}

func TestRotateIsIdempotent(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	for _, now := range []time.Time{t0, t0, t0.Add(29 * day), t0.Add(29 * day)} {
		if err := Rotate(ctx, c, rand.Reader, now); err != nil {
			t.Fatal(err)
		}
	}
	var list v1alpha1.SigningKeyList
	if err := c.List(ctx, &list, client.InNamespace("bedrock-system")); err != nil || len(list.Items) != 2 {
		t.Fatalf("two rotations twice must give two keys, got %d, err %v", len(list.Items), err)
	}
}

func TestRotateOnAlreadyExistsReturnsNilAndDeletesNothing(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	orphan := &v1alpha1.SigningKey{
		ObjectMeta: objectMeta("k-20260928t120000z"),
		Spec:       v1alpha1.SigningKeySpec{Algorithm: v1alpha1.AlgorithmES256, SecretRef: "k-20260928t120000z", NotBefore: metav1.NewTime(t0), RetireAfter: metav1.NewTime(t0.Add(keys.Lifetime))},
	}
	create(t, c, orphan)
	if err := Rotate(ctx, c, rand.Reader, t0.Add(500*time.Millisecond)); err != nil {
		t.Fatalf("an AlreadyExists from Save must not be an error, another replica already won: %v", err)
	}
	var got v1alpha1.SigningKey
	if err := c.Get(ctx, objectKey(orphan.Name), &got); err != nil {
		t.Fatalf("Rotate must not delete the existing SigningKey on AlreadyExists: %v", err)
	}
}

func TestRotateAlreadyExistsFromAWinningReplicaIsNotAnError(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	hook := &hookClient{Client: c}
	hook.afterList = func() {
		winner, err := keys.Generate(rand.Reader, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := keys.Save(ctx, c, winner); err != nil {
			t.Fatal(err)
		}
	}
	if err := Rotate(ctx, hook, rand.Reader, t0); err != nil {
		t.Fatalf("an AlreadyExists from a replica that already saved the same key must not be an error: %v", err)
	}
	loaded, err := keys.Load(ctx, c)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("exactly one key must exist, got %v, err %v", loaded, err)
	}
}

func TestRotateTwoGoroutinesProduceOneKey(t *testing.T) {
	c, _ := startTestEnv(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = Rotate(ctx, c, rand.Reader, t0)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	loaded, err := keys.Load(ctx, c)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("two concurrent rotations must leave exactly one key, got %v, err %v", loaded, err)
	}
}
