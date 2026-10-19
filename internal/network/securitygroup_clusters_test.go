package network

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSecurityGroupDeletionWaitsForKubernetesClusters(t *testing.T) {
	c, ns := groupFixture(t)
	ctx := context.Background()
	control := securityGroupOf(ns, "control", nil, nil)
	workers := securityGroupOf(ns, "workers", nil, nil)
	requireAccepted(t, c, control)
	requireAccepted(t, c, workers)
	cluster := &v1alpha1.KubernetesCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "prod"},
		Spec: v1alpha1.KubernetesClusterSpec{
			Version:      "v1.36.3+k0s.1",
			ControlPlane: v1alpha1.KubernetesControlPlane{SecurityGroups: []string{"control"}},
			Workers: []v1alpha1.KubernetesWorker{{
				Name: "a", Image: v1alpha1.ImageRef{Name: "ubuntu"}, SecurityGroups: []string{"workers"},
			}},
		},
	}
	requireAccepted(t, c, cluster)
	for _, name := range []string{"control", "workers"} {
		reconcileSecurityGroup(t, c, ns, name)
		group := securityGroupOf(ns, name, nil, nil)
		if err := c.Get(ctx, client.ObjectKeyFromObject(group), group); err != nil {
			t.Fatal(err)
		}
		if err := c.Delete(ctx, group); err != nil {
			t.Fatal(err)
		}
		reconcileSecurityGroup(t, c, ns, name)
		requireGroupCondition(t, c, ns, name, metav1.ConditionFalse, ReasonInUse)
		var held v1alpha1.SecurityGroup
		if err := c.Get(ctx, client.ObjectKeyFromObject(group), &held); err != nil {
			t.Fatal(err)
		}
		if message := meta.FindStatusCondition(held.Status.Conditions, v1alpha1.ConditionReady).Message; !strings.Contains(message, "KubernetesCluster/prod") {
			t.Fatalf("message %q", message)
		}
	}
	if err := c.Delete(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control", "workers"} {
		reconcileSecurityGroup(t, c, ns, name)
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &v1alpha1.SecurityGroup{}); !apierrors.IsNotFound(err) {
			t.Fatalf("the group %s must be gone: %v", name, err)
		}
	}
}
