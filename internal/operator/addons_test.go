package operator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func probeConfigMap(in AddonInput) (Rendered, error) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "addon-probe", "namespace": release.SystemNamespace},
		"data":     map[string]any{"vip": in.Cluster.Spec.API.VIP, "replicas": in.Settings["storage.replicas"]},
	}}
	gate := func(live *unstructured.Unstructured) release.Readiness {
		ready, _, _ := unstructured.NestedString(live.Object, "data", "ready")
		if ready == "true" {
			return release.Readiness{Ready: true}
		}
		return release.Readiness{Message: "addon-probe not marked ready"}
	}
	probe := Probe{GVK: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, Key: client.ObjectKey{Namespace: release.SystemNamespace, Name: "addon-probe"}, Gate: gate}
	return Rendered{Objects: []*unstructured.Unstructured{obj}, Probes: []Probe{probe}}, nil
}

func skipAlways(AddonInput) (Rendered, error) {
	return Rendered{SkipReason: "NoDevices", SkipMessage: "no host lists a device"}, nil
}

func skipWithCondition(AddonInput) (Rendered, error) {
	extra := metav1.Condition{Type: "ExtraState", Status: metav1.ConditionFalse, Reason: "WaitingForThing", Message: "waits for the thing"}
	return Rendered{SkipReason: "NoDevices", SkipMessage: "no host lists a device", Conditions: []metav1.Condition{extra}}, nil
}

func renderUnknownKind(AddonInput) (Rendered, error) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"name": "w"}}}
	return Rendered{Objects: []*unstructured.Unstructured{obj}}, nil
}

func clusterCondition(t *testing.T, c client.Client, conditionType string) *metav1.Condition {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(context.Background(), client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return nil
	}
	for i := range cluster.Status.Conditions {
		if cluster.Status.Conditions[i].Type == conditionType {
			return &cluster.Status.Conditions[i]
		}
	}
	return nil
}

func TestAddonReconcilerAppliesProbesAndSkips(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: "dev", API: v1alpha1.APISpec{VIP: "10.0.0.250"}}}
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	addons := []Addon{
		{Name: "probe", Condition: "ProbeReady", Render: probeConfigMap},
		{Name: "skip", Condition: "SkipReady", Render: skipAlways},
		{Name: "extra", Condition: "ExtraReady", Render: skipWithCondition},
		{Name: "unknown", Condition: "UnknownReady", Render: renderUnknownKind},
	}
	if err := (&AddonReconciler{Client: mgr.GetClient(), Addons: addons, Interval: time.Second, ReadyInterval: time.Second}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	time.Sleep(2 * time.Second)
	if clusterCondition(t, c, "ProbeReady") != nil {
		t.Fatal("addons must wait for the release to be installed")
	}
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Status.Version = "dev"
	if err := c.Status().Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		cond := clusterCondition(t, c, "ProbeReady")
		return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "Progressing"
	})
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: "addon-probe"}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["vip"] != "10.0.0.250" || cm.Data["replicas"] != "1" {
		t.Fatalf("rendered data %+v", cm.Data)
	}
	waitFor(t, func() bool {
		cond := clusterCondition(t, c, "SkipReady")
		return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "NoDevices" && cond.Message == "no host lists a device"
	})
	waitFor(t, func() bool {
		cond := clusterCondition(t, c, "ExtraState")
		return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "WaitingForThing" && cond.Message == "waits for the thing" && cond.ObservedGeneration == cluster.Generation
	})
	waitFor(t, func() bool {
		cond := clusterCondition(t, c, "ExtraReady")
		return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "NoDevices"
	})
	waitFor(t, func() bool {
		cond := clusterCondition(t, c, "UnknownReady")
		return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "MissingCRD"
	})
	cm.Data["ready"] = "true"
	if err := c.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		cond := clusterCondition(t, c, "ProbeReady")
		return cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == "Ready" && cond.ObservedGeneration == cluster.Generation
	})
}

func TestAddonInputReadsThePlatformCA(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	cluster := v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{API: v1alpha1.APISpec{VIP: "10.0.0.250"}}}
	r := &AddonReconciler{Client: c}
	in, err := r.input(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if in.PlatformCA != nil {
		t.Fatal("no CA Secret yet")
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cert-manager", Name: "bedrock-ca"}, Data: map[string][]byte{"ca.crt": []byte("CA")}}); err != nil {
		t.Fatal(err)
	}
	in, err = r.input(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if in.PlatformCA == nil || string(in.PlatformCA.Data["ca.crt"]) != "CA" {
		t.Fatalf("platform CA %+v", in.PlatformCA)
	}
}

func TestAddonInputReadsTheRookMonFSIDOnlyWhenAdopting(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	plain := v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{API: v1alpha1.APISpec{VIP: "10.0.0.250"}}}
	adopting := *plain.DeepCopy()
	adopting.Spec.Storage.Adopt = adoptSpec()
	r := &AddonReconciler{Client: c}
	in, err := r.input(ctx, adopting)
	if err != nil {
		t.Fatal(err)
	}
	if in.CephMonFSID != "" {
		t.Fatal("no mon Secret yet")
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph"}}); err != nil {
		t.Fatal(err)
	}
	const monKey, cephKey = "AQBt3W5nAAAAABAAmonSecretThatMustNeverShowUp7A==", "AQDm9kR0Znq2LhAAadminSecretThatMustNeverShowUp0g=="
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon"}, Data: map[string][]byte{"fsid": []byte(adoptFSID), "mon-secret": []byte(monKey), "ceph-secret": []byte(cephKey)}}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	in, err = r.input(ctx, adopting)
	if err != nil {
		t.Fatal(err)
	}
	if in.CephMonFSID != adoptFSID {
		t.Fatalf("mon fsid %q", in.CephMonFSID)
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{monKey, cephKey} {
		if bytes.Contains(encoded, []byte(key)) || bytes.Contains(encoded, []byte(base64.StdEncoding.EncodeToString([]byte(key)))) {
			t.Fatal("the addon input carries key material of the mon Secret")
		}
	}
	in, err = r.input(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if in.CephMonFSID != "" {
		t.Fatal("a cluster that does not adopt must not read the mon Secret")
	}
}

func TestAddonProbeReportsTheReasonOfTheGate(t *testing.T) {
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: "addon-probe"}}
	r := &AddonReconciler{Client: fake.NewClientBuilder().WithObjects(configMap).Build()}
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: "addon-probe"}
	named := func(reason string) Probe {
		gate := func(*unstructured.Unstructured) release.Readiness {
			return release.Readiness{Reason: reason, Message: "not yet"}
		}
		return Probe{GVK: gvk, Key: key, Gate: gate}
	}
	addon := Addon{Name: "probe", Condition: "ProbeReady"}
	cases := map[string]string{"": "Progressing", "OSDsNotUp": "OSDsNotUp"}
	for reason, want := range cases {
		cond := r.probe(context.Background(), addon, []Probe{named(reason)})
		if cond.Status != metav1.ConditionFalse || cond.Reason != want || cond.Message != "not yet" {
			t.Fatalf("gate reason %q: condition %+v", reason, cond)
		}
	}
}

func TestAddonRemovesAbsentObjects(t *testing.T) {
	stale := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: "stale"}}
	c := fake.NewClientBuilder().WithObjects(stale).Build()
	r := &AddonReconciler{Client: c}
	absent := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name, "namespace": release.SystemNamespace}}}
	}
	rendered := Rendered{Absent: []*unstructured.Unstructured{absent("stale"), absent("never-existed")}}
	for range 2 {
		condition, applied := r.applyRendered(context.Background(), Addon{Name: "x", Condition: "XReady"}, rendered)
		if !applied {
			t.Fatalf("condition %+v", condition)
		}
	}
	err := c.Get(context.Background(), client.ObjectKeyFromObject(stale), &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("stale object must be gone: %v", err)
	}
}
