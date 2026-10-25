package operator

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	k0sConfigNamespace = "kube-system"
	k0sConfigName      = "k0s"

	reasonNodePortsScoped      = "Scoped"
	reasonNoManagementCIDR     = "NoManagementCIDR"
	reasonK0sConfigUnavailable = "K0sConfigUnavailable"
	reasonNodePortsPatchFailed = "PatchFailed"
	reasonNodePortsNotApplied  = "NotApplied"
)

var k0sClusterConfigGVK = schema.GroupVersionKind{Group: "k0s.k0sproject.io", Version: "v1beta1", Kind: "ClusterConfig"}

func (r *ClusterReconciler) reconcileNodePorts(ctx context.Context) error {
	var cluster v1alpha1.Cluster
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	reason, message, err := r.scopeNodePorts(ctx, cluster.Spec.ManagementCIDR)
	status := metav1.ConditionTrue
	if reason != reasonNodePortsScoped {
		status = metav1.ConditionFalse
	}
	if writeErr := r.writeStatus(ctx, func(s *v1alpha1.ClusterStatus) {
		setCondition(s, v1alpha1.ConditionNodePortsScoped, status, reason, message, cluster.Generation)
	}); writeErr != nil && err == nil {
		return writeErr
	}
	return err
}

func (r *ClusterReconciler) scopeNodePorts(ctx context.Context, cidr string) (string, string, error) {
	live, found, err := readK0sClusterConfig(ctx, r.APIReader)
	if err != nil {
		return reasonK0sConfigUnavailable, err.Error(), err
	}
	if !found {
		return reasonK0sConfigUnavailable, fmt.Sprintf("k0s ClusterConfig %s/%s not found: the controllers must run with dynamic config", k0sConfigNamespace, k0sConfigName), nil
	}
	if nodePortsScoped(live) {
		return reasonNodePortsScoped, "", nil
	}
	if cidr == "" {
		return reasonNoManagementCIDR, "set spec.managementCIDR to the underlay CIDR so node ports leave the tenant legs", nil
	}
	if err := patchNodePortAddresses(ctx, r.Client, live, cidr); err != nil {
		return reasonNodePortsPatchFailed, err.Error(), err
	}
	verified, found, err := readK0sClusterConfig(ctx, r.APIReader)
	if err != nil {
		return reasonK0sConfigUnavailable, err.Error(), err
	}
	if !found || !nodePortsScoped(verified) {
		err := fmt.Errorf("k0s ClusterConfig %s/%s has no kubeProxy.nodePortAddresses after the patch", k0sConfigNamespace, k0sConfigName)
		return reasonNodePortsNotApplied, err.Error(), err
	}
	return reasonNodePortsScoped, "", nil
}

func readK0sClusterConfig(ctx context.Context, reader client.Reader) (*unstructured.Unstructured, bool, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(k0sClusterConfigGVK)
	err := reader.Get(ctx, client.ObjectKey{Namespace: k0sConfigNamespace, Name: k0sConfigName}, live)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	return live, err == nil, err
}

func nodePortsScoped(config *unstructured.Unstructured) bool {
	disabled, _, _ := unstructured.NestedBool(config.Object, "spec", "network", "kubeProxy", "disabled")
	addresses, _, _ := unstructured.NestedSlice(config.Object, "spec", "network", "kubeProxy", "nodePortAddresses")
	return disabled || len(addresses) > 0
}

func patchNodePortAddresses(ctx context.Context, c client.Client, live *unstructured.Unstructured, cidr string) error {
	body, err := json.Marshal(map[string]any{"spec": map[string]any{"network": map[string]any{"kubeProxy": map[string]any{"nodePortAddresses": []string{cidr}}}}})
	if err != nil {
		return err
	}
	return c.Patch(ctx, live, client.RawPatch(types.MergePatchType, body))
}
