package addons

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type podSelector struct {
	Namespace string
	Selector  labels.Selector
}

func selectorsOf(objects []*unstructured.Unstructured) []podSelector {
	var selectors []podSelector
	for _, obj := range objects {
		if !workloadKinds[obj.GetKind()] {
			continue
		}
		match, found, _ := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels")
		if found && len(match) > 0 {
			selectors = append(selectors, podSelector{Namespace: obj.GetNamespace(), Selector: labels.SelectorFromSet(match)})
		}
	}
	return selectors
}

func pruneStep(ctx context.Context, c client.Client, previous release.Inventory, selectors []podSelector) (bool, []string, error) {
	if len(previous) == 0 {
		return true, nil, nil
	}
	if err := deleteWorkloads(ctx, c, previous); err != nil {
		return false, nil, err
	}
	waiting, err := podsOf(ctx, c, selectors)
	if err != nil {
		return false, nil, err
	}
	if len(waiting) > 0 {
		return false, waiting, nil
	}
	if err := (release.Applier{Client: c}).Prune(ctx, previous, release.Inventory{}); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func deleteWorkloads(ctx context.Context, c client.Client, previous release.Inventory) error {
	for _, ref := range sortedRefs(previous) {
		if !workloadKinds[ref.Kind] {
			continue
		}
		workload := &unstructured.Unstructured{}
		workload.SetAPIVersion(ref.APIVersion)
		workload.SetKind(ref.Kind)
		workload.SetNamespace(ref.Namespace)
		workload.SetName(ref.Name)
		if err := c.Delete(ctx, workload, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func podsOf(ctx context.Context, c client.Client, selectors []podSelector) ([]string, error) {
	var names []string
	for _, selector := range selectors {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(selector.Namespace), client.MatchingLabelsSelector{Selector: selector.Selector}); err != nil {
			return nil, err
		}
		for _, pod := range pods.Items {
			names = append(names, pod.Namespace+"/"+pod.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}
