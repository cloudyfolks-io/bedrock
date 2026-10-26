package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	k0sCoreDNSManifestDir = "var/lib/k0s/manifests/coredns"
	k0sStackLabel         = "k0s.k0sproject.io/stack"
	k0sCoreDNSStack       = "coredns"
	takeoverDone          = "done"
)

var k0sCoreDNSKinds = []schema.GroupVersionKind{
	{Version: "v1", Kind: "ServiceAccount"},
	{Version: "v1", Kind: "ConfigMap"},
	{Version: "v1", Kind: "Service"},
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
}

func removeStaleCoreDNSManifests(ctx context.Context, env StepEnv) error {
	dir := filepath.Join(env.Deps.Root, k0sCoreDNSManifestDir)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var cluster v1alpha1.Cluster
	if err := env.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return err
	}
	if cluster.Annotations[v1alpha1.AnnotationK0sCoreDNSTakeover] != takeoverDone {
		return nil
	}
	if err := requireNoK0sCoreDNSObjects(ctx, env.Client); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func requireNoK0sCoreDNSObjects(ctx context.Context, c client.Client) error {
	for _, gvk := range k0sCoreDNSKinds {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := c.List(ctx, list, client.MatchingLabels{k0sStackLabel: k0sCoreDNSStack}); err != nil {
			return err
		}
		if len(list.Items) > 0 {
			item := list.Items[0]
			return fmt.Errorf("k0s owns %s %s/%s again, and removing its CoreDNS manifests would make k0s delete it. Repair by hand: run `%s` for every object with the label, then run the Prune step again", gvk.Kind, item.GetNamespace(), item.GetName(), labelRepairCommand(&item))
		}
	}
	return nil
}

func labelRepairCommand(object *unstructured.Unstructured) string {
	target := strings.ToLower(object.GetKind()) + " " + object.GetName()
	if object.GetNamespace() != "" {
		return "kubectl -n " + object.GetNamespace() + " label " + target + " " + k0sStackLabel + "-"
	}
	return "kubectl label " + target + " " + k0sStackLabel + "-"
}
