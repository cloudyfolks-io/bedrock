package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	e2eTarget     = "s3://seaweedfs.backup-e2e.svc:8333/bedrock-backups?insecure=true"
	e2eSecretName = "backup-e2e"
)

func setSetting(t *testing.T, key, value string) {
	t.Helper()
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(setting), setting)
	if errors.IsNotFound(err) {
		setting.Spec.Value = value
		if err := k8sClient.Create(ctx, setting); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = value
	if err := k8sClient.Update(ctx, setting); err != nil {
		t.Fatal(err)
	}
}

func setCredentialsSecret(t *testing.T, data map[string]string) {
	t.Helper()
	ctx := context.Background()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: e2eSecretName, Namespace: SystemNamespace}, StringData: data}
	_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: e2eSecretName, Namespace: SystemNamespace}})
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
}

func validCredentials() map[string]string {
	return map[string]string{"access-key-id": "AK", "secret-access-key": "SK", "repository-password": "RP"}
}

func resetTargetState(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	location := &unstructured.Unstructured{}
	location.SetGroupVersionKind(schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: "BackupStorageLocation"})
	location.SetNamespace(VeleroNamespace)
	location.SetName(LocationName)
	_ = k8sClient.Delete(ctx, location)
	for _, name := range []string{CredentialsSecretName, RepositorySecretName, "other"} {
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: VeleroNamespace}})
	}
	class := &unstructured.Unstructured{}
	class.SetGroupVersionKind(schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClass"})
	class.SetName(SnapshotClassName)
	_ = k8sClient.Delete(ctx, class)
}

func newTargetReconciler() (*TargetReconciler, *record.FakeRecorder) {
	recorder := record.NewFakeRecorder(50)
	return &TargetReconciler{Client: k8sClient, Recorder: recorder}, recorder
}

func reconcileTarget(t *testing.T, r *TargetReconciler) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

func getVelero(t *testing.T, kind, name string) (*unstructured.Unstructured, error) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: kind})
	err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: VeleroNamespace, Name: name}, u)
	return u, err
}

func requireGone(t *testing.T, err error) {
	t.Helper()
	if !errors.IsNotFound(err) {
		t.Fatalf("object must be gone, got %v", err)
	}
}

func TestTargetRendersTheLocation(t *testing.T) {
	resetTargetState(t)
	setSetting(t, "backup.target", e2eTarget)
	setSetting(t, "backup.credentials-secret", e2eSecretName)
	setCredentialsSecret(t, validCredentials())
	r, recorder := newTargetReconciler()
	result := reconcileTarget(t, r)
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	location, err := getVelero(t, "BackupStorageLocation", LocationName)
	if err != nil {
		t.Fatal(err)
	}
	bucket, _, _ := unstructured.NestedString(location.Object, "spec", "objectStorage", "bucket")
	s3Url, _, _ := unstructured.NestedString(location.Object, "spec", "config", "s3Url")
	if bucket != "bedrock-backups" || s3Url != "http://seaweedfs.backup-e2e.svc:8333" {
		t.Fatalf("location spec %v", location.Object["spec"])
	}
	credentials := &corev1.Secret{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: VeleroNamespace, Name: CredentialsSecretName}, credentials); err != nil {
		t.Fatal(err)
	}
	if string(credentials.Data[CredentialsKey]) != "[default]\naws_access_key_id=AK\naws_secret_access_key=SK\n" {
		t.Fatalf("credentials %q", credentials.Data[CredentialsKey])
	}
	repository := &corev1.Secret{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: VeleroNamespace, Name: RepositorySecretName}, repository); err != nil {
		t.Fatal(err)
	}
	if string(repository.Data[RepositoryKey]) != "RP" {
		t.Fatalf("repository %q", repository.Data[RepositoryKey])
	}
	class := &unstructured.Unstructured{}
	class.SetGroupVersionKind(schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClass"})
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: SnapshotClassName}, class); err != nil {
		t.Fatal(err)
	}
	if events := drainEvents(recorder); len(events) != 0 {
		t.Fatalf("events %v", events)
	}
}

func TestTargetRendersTheCABundle(t *testing.T) {
	resetTargetState(t)
	setSetting(t, "backup.target", e2eTarget)
	setSetting(t, "backup.credentials-secret", e2eSecretName)
	data := validCredentials()
	data["ca.crt"] = "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n"
	setCredentialsSecret(t, data)
	r, _ := newTargetReconciler()
	reconcileTarget(t, r)
	location, err := getVelero(t, "BackupStorageLocation", LocationName)
	if err != nil {
		t.Fatal(err)
	}
	ca, _, _ := unstructured.NestedString(location.Object, "spec", "objectStorage", "caCert")
	want := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCmFiYwotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="
	if ca != want {
		t.Fatalf("caCert %q", ca)
	}
}

func TestTargetKeepsTheLocationOnBadCredentials(t *testing.T) {
	resetTargetState(t)
	setSetting(t, "backup.target", e2eTarget)
	setSetting(t, "backup.credentials-secret", e2eSecretName)
	setCredentialsSecret(t, validCredentials())
	r, recorder := newTargetReconciler()
	reconcileTarget(t, r)
	before, err := getVelero(t, "BackupStorageLocation", LocationName)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(recorder)
	broken := validCredentials()
	delete(broken, "repository-password")
	setCredentialsSecret(t, broken)
	reconcileTarget(t, r)
	after, err := getVelero(t, "BackupStorageLocation", LocationName)
	if err != nil {
		t.Fatal(err)
	}
	if after.GetResourceVersion() != before.GetResourceVersion() {
		t.Fatal("location changed")
	}
	repository := &corev1.Secret{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: VeleroNamespace, Name: RepositorySecretName}, repository); err != nil || string(repository.Data[RepositoryKey]) != "RP" {
		t.Fatalf("repository secret changed: %v", err)
	}
	events := drainEvents(recorder)
	if len(events) != 1 || !strings.Contains(events[0], "Warning") || !strings.Contains(events[0], "BackupCredentialsInvalid") || !strings.Contains(events[0], "repository-password") {
		t.Fatalf("events %v", events)
	}
}

func TestTargetEmptyRemovesTheLocation(t *testing.T) {
	resetTargetState(t)
	setSetting(t, "backup.target", e2eTarget)
	setSetting(t, "backup.credentials-secret", e2eSecretName)
	setCredentialsSecret(t, validCredentials())
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: VeleroNamespace}}
	if err := k8sClient.Create(context.Background(), other); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	r, _ := newTargetReconciler()
	reconcileTarget(t, r)
	setSetting(t, "backup.target", "")
	reconcileTarget(t, r)
	_, err := getVelero(t, "BackupStorageLocation", LocationName)
	requireGone(t, err)
	for _, name := range []string{CredentialsSecretName, RepositorySecretName} {
		err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: VeleroNamespace, Name: name}, &corev1.Secret{})
		requireGone(t, err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: VeleroNamespace, Name: "other"}, &corev1.Secret{}); err != nil {
		t.Fatalf("foreign secret removed: %v", err)
	}
}

func TestTargetIgnoresNFS(t *testing.T) {
	resetTargetState(t)
	setSetting(t, "backup.target", "nfs://nas.example.com/export")
	setSetting(t, "backup.credentials-secret", e2eSecretName)
	setCredentialsSecret(t, validCredentials())
	r, recorder := newTargetReconciler()
	reconcileTarget(t, r)
	_, err := getVelero(t, "BackupStorageLocation", LocationName)
	requireGone(t, err)
	events := drainEvents(recorder)
	if len(events) != 1 || !strings.Contains(events[0], "BackupTargetInvalid") || !strings.Contains(events[0], "nfs targets are not supported") {
		t.Fatalf("events %v", events)
	}
}

func TestTargetEventsHideSecretValues(t *testing.T) {
	resetTargetState(t)
	setSetting(t, "backup.target", e2eTarget)
	setSetting(t, "backup.credentials-secret", e2eSecretName)
	broken := validCredentials()
	delete(broken, "secret-access-key")
	setCredentialsSecret(t, broken)
	r, recorder := newTargetReconciler()
	reconcileTarget(t, r)
	setSetting(t, "backup.target", "nfs://nas/export")
	reconcileTarget(t, r)
	for _, event := range drainEvents(recorder) {
		for _, secret := range []string{"AK", "SK", "RP"} {
			if strings.Contains(event, secret) {
				t.Fatalf("event %q leaks %s", event, secret)
			}
		}
	}
}

func TestTargetWithoutVeleroCRDsRequeues(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "backup.target"}, Spec: v1alpha1.SettingSpec{Value: e2eTarget}},
		&v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "backup.credentials-secret"}, Spec: v1alpha1.SettingSpec{Value: e2eSecretName}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: e2eSecretName, Namespace: SystemNamespace}, Data: map[string][]byte{"access-key-id": []byte("AK"), "secret-access-key": []byte("SK"), "repository-password": []byte("RP")}},
	}
	missing := func(context.Context, client.WithWatch, runtime.ApplyConfiguration, ...client.ApplyOption) error {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "velero.io", Kind: "BackupStorageLocation"}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{Apply: missing}).Build()
	recorder := record.NewFakeRecorder(10)
	r := &TargetReconciler{Client: c, Recorder: recorder}
	result, err := r.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue %v", result.RequeueAfter)
	}
	if events := drainEvents(recorder); len(events) != 0 {
		t.Fatalf("events %v", events)
	}
}
