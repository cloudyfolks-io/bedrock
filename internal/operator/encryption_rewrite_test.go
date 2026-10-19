package operator

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

var secretsResource = schema.GroupResource{Resource: "secrets"}

type scriptedWriter struct {
	client.Client
	updates   map[string]int
	conflicts map[string]int
	broken    map[string]bool
	moved     map[string]bool
}

func (w *scriptedWriter) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	name := obj.GetNamespace() + "/" + obj.GetName()
	w.updates[name]++
	if w.broken[name] {
		return errors.New("webhook refused")
	}
	if w.conflicts[name] > 0 {
		w.conflicts[name]--
		return apierrors.NewConflict(secretsResource, obj.GetName(), errors.New("changed"))
	}
	err := w.Client.Update(ctx, obj, opts...)
	if err == nil && w.moved[name] {
		obj.SetResourceVersion(obj.GetResourceVersion() + "-moved")
	}
	return err
}

func newScriptedWriter(c client.Client) *scriptedWriter {
	return &scriptedWriter{Client: c, updates: map[string]int{}, conflicts: map[string]int{}, broken: map[string]bool{}, moved: map[string]bool{}}
}

func TestRewriteSecretsCountsOnlyRealUpdates(t *testing.T) {
	c, _ := StartTestEnv(t)
	createSecrets(t, c, "count-a", 3)
	writer := newScriptedWriter(c)
	writer.moved["count-a/sa"] = true
	writer.moved["count-a/sb"] = true
	result, err := rewriteSecrets(context.Background(), c, writer, 2)
	if err != nil || result.Updated != 2 || len(result.Failed) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestRewriteSecretsRetriesAConflictWithAFreshRead(t *testing.T) {
	c, _ := StartTestEnv(t)
	createSecrets(t, c, "conflict-a", 2)
	writer := newScriptedWriter(c)
	writer.conflicts["conflict-a/sa"] = 2
	writer.moved["conflict-a/sa"] = true
	result, err := rewriteSecrets(context.Background(), c, writer, 10)
	if err != nil || result.Updated != 1 || len(result.Failed) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if writer.updates["conflict-a/sa"] != 3 {
		t.Fatalf("updates %d", writer.updates["conflict-a/sa"])
	}
}

func TestRewriteSecretsReportsAFailedSecretAndGoesOn(t *testing.T) {
	c, _ := StartTestEnv(t)
	createSecrets(t, c, "fail-a", 3)
	writer := newScriptedWriter(c)
	writer.broken["fail-a/sb"] = true
	writer.moved["fail-a/sa"] = true
	writer.moved["fail-a/sc"] = true
	result, err := rewriteSecrets(context.Background(), c, writer, 10)
	if err != nil || result.Updated != 2 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if len(result.Failed) != 1 || result.Failed[0] != "fail-a/sb" {
		t.Fatalf("failed %v", result.Failed)
	}
}

type expiringReader struct {
	client.Reader
	expired bool
}

func (r *expiringReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var options client.ListOptions
	options.ApplyOptions(opts)
	if options.Continue != "" && !r.expired {
		r.expired = true
		return apierrors.NewResourceExpired("the continue token expired")
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestRewriteSecretsRestartsAnExpiredListAndSkipsDoneSecrets(t *testing.T) {
	c, _ := StartTestEnv(t)
	createSecrets(t, c, "expire-a", 5)
	var all corev1.SecretList
	if err := c.List(context.Background(), &all); err != nil {
		t.Fatal(err)
	}
	writer := newScriptedWriter(c)
	for index := range all.Items {
		writer.moved[all.Items[index].Namespace+"/"+all.Items[index].Name] = true
	}
	result, err := rewriteSecrets(context.Background(), &expiringReader{Reader: c}, writer, 2)
	if err != nil || result.Updated != int64(len(all.Items)) {
		t.Fatalf("result %+v err %v", result, err)
	}
	for name, count := range writer.updates {
		if count != 1 {
			t.Errorf("secret %s updated %d times", name, count)
		}
	}
}

func driveToRewriting(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	for range 40 {
		if _, err := encryptionReconciler(c).reconcileEncryption(ctx); err != nil {
			t.Fatal(err)
		}
		if status := currentCluster(t, c).Status.Encryption; status != nil && status.Phase == v1alpha1.RotationRewritingSecrets {
			applyGrants(t, c)
			return
		}
		applyGrants(t, c)
	}
	t.Fatal("the rotation did not reach the rewrite")
}

func TestARewriteFailureStallsTheRotationWithACondition(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	chain := newConfigChain(t)
	cluster := newCluster("v0.1.0")
	cluster.Spec.Encryption.Action = v1alpha1.EncryptionActionRotate
	createValid(t, c, cluster)
	createEncryptionSecret(t, c, chain.one)
	createControlPlane(t, c, "cp-1", encryption.Hash(chain.one))
	createSecrets(t, c, "stall-a", 2)
	driveToRewriting(t, c)

	writer := newScriptedWriter(c)
	writer.broken["stall-a/sb"] = true
	broken := &ClusterReconciler{Client: writer, APIReader: c, Interval: encryptionReconciler(c).Interval}
	if _, err := broken.reconcileEncryption(ctx); err != nil {
		t.Fatal(err)
	}
	got := currentCluster(t, c)
	condition := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionEncryptionStalled)
	if got.Status.Encryption.Phase != v1alpha1.RotationRewritingSecrets || condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "SecretsFailed" || !strings.Contains(condition.Message, "stall-a/sb") {
		t.Fatalf("status %+v condition %+v", got.Status.Encryption, condition)
	}
	for range 10 {
		if _, err := encryptionReconciler(c).reconcileEncryption(ctx); err != nil {
			t.Fatal(err)
		}
		applyGrants(t, c)
		if status := currentCluster(t, c).Status.Encryption; status != nil && status.Phase == "" {
			break
		}
	}
	got = currentCluster(t, c)
	condition = meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionEncryptionStalled)
	if got.Status.Encryption.Phase != "" || condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("status %+v condition %+v", got.Status.Encryption, condition)
	}
}

func TestAFailedHostSetsTheStalledCondition(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	chain := newConfigChain(t)
	cluster := newCluster("v0.1.0")
	cluster.Spec.Encryption.Action = v1alpha1.EncryptionActionRotate
	createValid(t, c, cluster)
	createEncryptionSecret(t, c, chain.one)
	createControlPlane(t, c, "cp-1", encryption.Hash(chain.one))
	reconciler := encryptionReconciler(c)
	for range 3 {
		if _, err := reconciler.reconcileEncryption(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var host v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "cp-1"}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Encryption.FailedHash = host.Annotations[v1alpha1.AnnotationEncryptionApply]
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.reconcileEncryption(ctx); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(currentCluster(t, c).Status.Conditions, v1alpha1.ConditionEncryptionStalled)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "HostFailed" || !strings.Contains(condition.Message, "cp-1") {
		t.Fatalf("condition %+v", condition)
	}
}
