package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

func TestNodeUpgradeValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	upgrade := &v1alpha1.NodeUpgrade{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v0.3.0", "node-a")},
		Spec:       v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1, Steps: []string{v1alpha1.StepPreload}},
	}
	if err := c.Create(ctx, upgrade); err != nil {
		t.Fatal(err)
	}
	upgrade.Spec.Steps = append(upgrade.Spec.Steps, v1alpha1.StepBackup)
	upgrade.Spec.Backup = "/var/lib/bedrock/backups/a.tar.gz"
	upgrade.Spec.Attempt = 2
	if err := c.Update(ctx, upgrade); err != nil {
		t.Fatalf("appending a step, setting the backup and raising the attempt must be allowed: %v", err)
	}
	cases := map[string]func(*v1alpha1.NodeUpgrade){
		"change node":    func(u *v1alpha1.NodeUpgrade) { u.Spec.Node = "node-b" },
		"change version": func(u *v1alpha1.NodeUpgrade) { u.Spec.Version = "v0.4.0" },
		"drop a step":    func(u *v1alpha1.NodeUpgrade) { u.Spec.Steps = []string{v1alpha1.StepBackup} },
		"change backup":  func(u *v1alpha1.NodeUpgrade) { u.Spec.Backup = "/var/lib/bedrock/backups/b.tar.gz" },
		"lower attempt":  func(u *v1alpha1.NodeUpgrade) { u.Spec.Attempt = 1 },
		"unknown step":   func(u *v1alpha1.NodeUpgrade) { u.Spec.Steps = append(u.Spec.Steps, "Explode") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var current v1alpha1.NodeUpgrade
			if err := c.Get(ctx, client.ObjectKeyFromObject(upgrade), &current); err != nil {
				t.Fatal(err)
			}
			mutate(&current)
			if err := c.Update(ctx, &current); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestNodeUpgradeSelectableByNode(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	for _, node := range []string{"node-a", "node-b"} {
		upgrade := &v1alpha1.NodeUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v0.3.0", node)},
			Spec:       v1alpha1.NodeUpgradeSpec{Node: node, Version: "v0.3.0", From: "v0.2.0", Attempt: 1},
		}
		if err := c.Create(ctx, upgrade); err != nil {
			t.Fatal(err)
		}
	}
	var list v1alpha1.NodeUpgradeList
	selector := client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.node", "node-b")}
	if err := c.List(ctx, &list, selector); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Spec.Node != "node-b" {
		t.Fatalf("field selector spec.node=node-b returned %+v", list.Items)
	}
}
