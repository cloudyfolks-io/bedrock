package release

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Readiness struct {
	Ready   bool
	Failed  bool
	Message string
}

type Gate func(obj *unstructured.Unstructured) Readiness

type Gates map[schema.GroupKind]Gate

func Check(gates Gates, obj *unstructured.Unstructured) Readiness {
	if gate, ok := gates[obj.GroupVersionKind().GroupKind()]; ok {
		return gate(obj)
	}
	return DefaultGate(obj)
}

func DefaultGate(obj *unstructured.Unstructured) Readiness {
	switch obj.GroupVersionKind().GroupKind() {
	case schema.GroupKind{Group: "apps", Kind: "Deployment"}:
		return generationCurrent(obj, deploymentReady(obj, specReplicas(obj)))
	case schema.GroupKind{Group: "apps", Kind: "StatefulSet"}:
		return generationCurrent(obj, replicasReady(obj, "readyReplicas", "updatedReplicas", specReplicas(obj)))
	case schema.GroupKind{Group: "apps", Kind: "DaemonSet"}:
		desired, _, _ := unstructured.NestedInt64(obj.Object, "status", "desiredNumberScheduled")
		return generationCurrent(obj, replicasReady(obj, "numberReady", daemonSetUpdatedField(obj), desired))
	case schema.GroupKind{Group: "batch", Kind: "Job"}:
		return jobReadiness(obj)
	case schema.GroupKind{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}:
		return conditionTrue(obj, "Established")
	}
	return Readiness{Ready: true}
}

func daemonSetUpdatedField(obj *unstructured.Unstructured) string {
	strategy, _, _ := unstructured.NestedString(obj.Object, "spec", "updateStrategy", "type")
	if strategy == "OnDelete" {
		return "numberReady"
	}
	return "updatedNumberScheduled"
}

func specReplicas(obj *unstructured.Unstructured) int64 {
	replicas, found, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if !found {
		return 1
	}
	return replicas
}

func deploymentReady(obj *unstructured.Unstructured, want int64) Readiness {
	replicas, _, _ := unstructured.NestedInt64(obj.Object, "status", "replicas")
	if replicas != want {
		return Readiness{Message: fmt.Sprintf("%s %d/%d replicas", obj.GetName(), replicas, want)}
	}
	return replicasReady(obj, "availableReplicas", "updatedReplicas", want)
}

func replicasReady(obj *unstructured.Unstructured, readyField, updatedField string, want int64) Readiness {
	ready, _, _ := unstructured.NestedInt64(obj.Object, "status", readyField)
	updated, _, _ := unstructured.NestedInt64(obj.Object, "status", updatedField)
	if ready == want && updated == want {
		return Readiness{Ready: true}
	}
	return Readiness{Message: fmt.Sprintf("%s %d/%d ready, %d updated", obj.GetName(), ready, want, updated)}
}

func generationCurrent(obj *unstructured.Unstructured, next Readiness) Readiness {
	observed, found, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	if !found {
		return Readiness{Message: fmt.Sprintf("%s status not reported yet", obj.GetName())}
	}
	if observed < obj.GetGeneration() {
		return Readiness{Message: fmt.Sprintf("%s observedGeneration %d behind %d", obj.GetName(), observed, obj.GetGeneration())}
	}
	return next
}

func jobReadiness(obj *unstructured.Unstructured) Readiness {
	if conditionTrue(obj, "Failed").Ready {
		return Readiness{Failed: true, Message: fmt.Sprintf("%s job failed", obj.GetName())}
	}
	return conditionTrue(obj, "Complete")
}

func conditionTrue(obj *unstructured.Unstructured, conditionType string) Readiness {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] == conditionType && condition["status"] == "True" {
			return Readiness{Ready: true}
		}
	}
	return Readiness{Message: fmt.Sprintf("%s condition %s not true", obj.GetName(), conditionType)}
}

var defaultGatedKinds = map[schema.GroupKind]struct{}{
	{Group: "apps", Kind: "Deployment"}:                               {},
	{Group: "apps", Kind: "StatefulSet"}:                              {},
	{Group: "apps", Kind: "DaemonSet"}:                                {},
	{Group: "batch", Kind: "Job"}:                                     {},
	{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}: {},
}

func gated(gates Gates, obj *unstructured.Unstructured) bool {
	gk := obj.GroupVersionKind().GroupKind()
	if _, ok := gates[gk]; ok {
		return true
	}
	_, ok := defaultGatedKinds[gk]
	return ok
}

func WaitGroup(ctx context.Context, c client.Client, gates Gates, group Group, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		pending, err := notReady(ctx, c, gates, group)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("group %s not ready: %s: %w", group.Name, strings.Join(pending, "; "), ctx.Err())
		case <-ticker.C:
		}
	}
}

func notReady(ctx context.Context, c client.Client, gates Gates, group Group) ([]string, error) {
	var pending []string
	for _, obj := range group.Objects {
		if !gated(gates, obj) {
			continue
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GroupVersionKind())
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			if errors.IsNotFound(err) {
				pending = append(pending, fmt.Sprintf("%s %s/%s not found yet", obj.GetKind(), obj.GetNamespace(), obj.GetName()))
				continue
			}
			return nil, fmt.Errorf("read %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
		r := Check(gates, live)
		if r.Failed {
			return nil, fmt.Errorf("%s %s/%s failed: %s", obj.GetKind(), obj.GetNamespace(), obj.GetName(), r.Message)
		}
		if !r.Ready {
			pending = append(pending, r.Message)
		}
	}
	return pending, nil
}

func PhaseGate(phase string) Gate {
	return func(obj *unstructured.Unstructured) Readiness {
		current, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if current == phase {
			return Readiness{Ready: true}
		}
		return Readiness{Message: fmt.Sprintf("%s phase %s, want %s", obj.GetName(), current, phase)}
	}
}

func ConditionGate(conditionType string) Gate {
	return func(obj *unstructured.Unstructured) Readiness {
		return conditionTrue(obj, conditionType)
	}
}
