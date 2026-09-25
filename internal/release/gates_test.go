package release

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func obj(apiVersion, kind string, spec, status map[string]any, generation int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": "x", "generation": generation},
		"spec":       spec,
		"status":     status,
	}}
	return u
}

func TestDefaultGateDeployment(t *testing.T) {
	ready := obj("apps/v1", "Deployment", map[string]any{"replicas": int64(2)}, map[string]any{"observedGeneration": int64(3), "replicas": int64(2), "availableReplicas": int64(2), "updatedReplicas": int64(2)}, 3)
	if r := DefaultGate(ready); !r.Ready {
		t.Fatalf("expected ready, got %q", r.Message)
	}
	stale := obj("apps/v1", "Deployment", map[string]any{"replicas": int64(2)}, map[string]any{"observedGeneration": int64(2), "replicas": int64(2), "availableReplicas": int64(2), "updatedReplicas": int64(2)}, 3)
	if r := DefaultGate(stale); r.Ready {
		t.Fatal("stale observedGeneration must not be ready")
	}
	rolling := obj("apps/v1", "Deployment", map[string]any{"replicas": int64(2)}, map[string]any{"observedGeneration": int64(3), "replicas": int64(2), "availableReplicas": int64(2), "updatedReplicas": int64(1)}, 3)
	if r := DefaultGate(rolling); r.Ready {
		t.Fatal("rolling update must not be ready")
	}
	defaulted := obj("apps/v1", "Deployment", map[string]any{}, map[string]any{"observedGeneration": int64(1), "replicas": int64(1), "availableReplicas": int64(1), "updatedReplicas": int64(1)}, 1)
	if r := DefaultGate(defaulted); !r.Ready {
		t.Fatalf("replicas defaults to 1: %q", r.Message)
	}
}

func TestDefaultGateDeploymentStatusReplicasMismatch(t *testing.T) {
	stuck := obj("apps/v1", "Deployment", map[string]any{"replicas": int64(3)}, map[string]any{"observedGeneration": int64(1), "replicas": int64(2), "availableReplicas": int64(2), "updatedReplicas": int64(2)}, 1)
	if r := DefaultGate(stuck); r.Ready {
		t.Fatal("status.replicas behind spec.replicas must not be ready")
	}
}

func TestDefaultGateDaemonSetAndStatefulSet(t *testing.T) {
	ds := obj("apps/v1", "DaemonSet", map[string]any{}, map[string]any{"observedGeneration": int64(1), "desiredNumberScheduled": int64(3), "numberReady": int64(3), "updatedNumberScheduled": int64(3)}, 1)
	if r := DefaultGate(ds); !r.Ready {
		t.Fatalf("daemonset: %q", r.Message)
	}
	ds.Object["status"].(map[string]any)["numberReady"] = int64(2)
	if r := DefaultGate(ds); r.Ready {
		t.Fatal("daemonset with 2/3 ready must not be ready")
	}
	onDelete := obj("apps/v1", "DaemonSet", map[string]any{"updateStrategy": map[string]any{"type": "OnDelete"}}, map[string]any{"observedGeneration": int64(2), "desiredNumberScheduled": int64(3), "numberReady": int64(3), "updatedNumberScheduled": int64(0)}, 2)
	if r := DefaultGate(onDelete); !r.Ready {
		t.Fatalf("an OnDelete daemonset is ready when every pod is ready: %q", r.Message)
	}
	onDelete.Object["status"].(map[string]any)["numberReady"] = int64(2)
	if r := DefaultGate(onDelete); r.Ready || r.Message != "x 2/3 ready, 2 updated" {
		t.Fatalf("an OnDelete daemonset with 2/3 ready: %+v", r)
	}
	rolling := obj("apps/v1", "DaemonSet", map[string]any{"updateStrategy": map[string]any{"type": "RollingUpdate"}}, map[string]any{"observedGeneration": int64(2), "desiredNumberScheduled": int64(3), "numberReady": int64(3), "updatedNumberScheduled": int64(0)}, 2)
	if r := DefaultGate(rolling); r.Ready {
		t.Fatal("a rolling daemonset needs every pod updated")
	}
	sts := obj("apps/v1", "StatefulSet", map[string]any{"replicas": int64(3)}, map[string]any{"observedGeneration": int64(1), "readyReplicas": int64(3), "updatedReplicas": int64(3)}, 1)
	if r := DefaultGate(sts); !r.Ready {
		t.Fatalf("statefulset: %q", r.Message)
	}
}

func TestDefaultGateConditions(t *testing.T) {
	crd := obj("apiextensions.k8s.io/v1", "CustomResourceDefinition", map[string]any{}, map[string]any{"conditions": []any{map[string]any{"type": "Established", "status": "True"}}}, 1)
	if r := DefaultGate(crd); !r.Ready {
		t.Fatalf("crd: %q", r.Message)
	}
	crd.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Established", "status": "False"}}
	if r := DefaultGate(crd); r.Ready {
		t.Fatal("unestablished crd must not be ready")
	}
	job := obj("batch/v1", "Job", map[string]any{}, map[string]any{"conditions": []any{map[string]any{"type": "Complete", "status": "True"}}}, 1)
	if r := DefaultGate(job); !r.Ready {
		t.Fatalf("job: %q", r.Message)
	}
	cm := obj("v1", "ConfigMap", nil, nil, 0)
	if r := DefaultGate(cm); !r.Ready {
		t.Fatal("configmap is always ready")
	}
}

func TestDefaultGateMissingObservedGeneration(t *testing.T) {
	ds := obj("apps/v1", "DaemonSet", map[string]any{}, map[string]any{}, 1)
	if r := DefaultGate(ds); r.Ready {
		t.Fatal("daemonset without observedGeneration must not be ready")
	}
	scaledDown := obj("apps/v1", "Deployment", map[string]any{"replicas": int64(0)}, map[string]any{}, 1)
	if r := DefaultGate(scaledDown); r.Ready {
		t.Fatal("deployment without observedGeneration must not be ready")
	}
	scaledDownReady := obj("apps/v1", "Deployment", map[string]any{"replicas": int64(0)}, map[string]any{"observedGeneration": int64(1), "availableReplicas": int64(0), "updatedReplicas": int64(0)}, 1)
	if r := DefaultGate(scaledDownReady); !r.Ready {
		t.Fatalf("deployment scaled to zero must be ready: %q", r.Message)
	}
}

func TestDefaultGateJobFailed(t *testing.T) {
	job := obj("batch/v1", "Job", map[string]any{}, map[string]any{"conditions": []any{map[string]any{"type": "Failed", "status": "True"}}}, 1)
	r := DefaultGate(job)
	if r.Ready || !r.Failed {
		t.Fatalf("failed job must report Failed: %+v", r)
	}
}

func TestWaitGroupRejectsNonPositiveInterval(t *testing.T) {
	err := WaitGroup(context.Background(), nil, Gates{}, Group{}, 0)
	if err == nil || err.Error() != "interval must be positive" {
		t.Fatalf("expected interval error, got %v", err)
	}
}

func TestCheckUsesOverride(t *testing.T) {
	gates := Gates{schema.GroupKind{Group: "", Kind: "ConfigMap"}: func(*unstructured.Unstructured) Readiness { return Readiness{Ready: false, Message: "override"} }}
	cm := obj("v1", "ConfigMap", nil, nil, 0)
	if r := Check(gates, cm); r.Ready || r.Message != "override" {
		t.Fatalf("override not applied: %+v", r)
	}
	if r := Check(Gates{}, cm); !r.Ready {
		t.Fatal("default must apply without override")
	}
}

func TestPhaseGate(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "kubevirt"}, "status": map[string]any{"phase": "Deploying"}}}
	if r := PhaseGate("Deployed")(obj); r.Ready || r.Failed || r.Message != "kubevirt phase Deploying, want Deployed" {
		t.Fatalf("readiness %+v", r)
	}
	_ = unstructured.SetNestedField(obj.Object, "Deployed", "status", "phase")
	if r := PhaseGate("Deployed")(obj); !r.Ready {
		t.Fatalf("readiness %+v", r)
	}
}

func TestConditionGate(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "platform-tls"}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	if r := ConditionGate("Ready")(obj); !r.Ready {
		t.Fatalf("readiness %+v", r)
	}
	if r := ConditionGate("Issuing")(obj); r.Ready {
		t.Fatalf("readiness %+v", r)
	}
}
