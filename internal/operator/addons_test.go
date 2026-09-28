package operator

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
