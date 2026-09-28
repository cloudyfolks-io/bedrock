package operator

import (
	"context"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestRatchetReplicas(t *testing.T) {
	cases := []struct {
		name      string
		current   string
		osdHosts  int
		ratcheted bool
		want      string
		write     bool
	}{
		{"below the threshold", "1", 2, false, "", false},
		{"already ratcheted", "1", 3, true, "", false},
		{"raises at the threshold", "1", 3, false, "3", true},
		{"raises an unset value", "", 3, false, "3", true},
		{"keeps a higher value", "5", 3, false, "5", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, write := ratchetReplicas(tc.current, tc.osdHosts, tc.ratcheted)
			if value != tc.want || write != tc.write {
				t.Fatalf("got %q %v, want %q %v", value, write, tc.want, tc.write)
			}
		})
	}
}

func TestStorageTopologyRatchetsReplicasOnce(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	setting := &v1alpha1.Setting{}
	setting.Name = storageReplicasKey
	setting.Spec.Value = "1"
	if err := c.Create(ctx, setting); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		host := osdHost(name, "/dev/sdb")
		if err := c.Create(ctx, &host); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&StorageTopologyReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	readSetting := func() v1alpha1.Setting {
		var current v1alpha1.Setting
		if err := c.Get(ctx, client.ObjectKey{Name: storageReplicasKey}, &current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	waitFor(t, func() bool {
		current := readSetting()
		return current.Spec.Value == "3" && current.Annotations[replicasRatchetedAnnotation] != ""
	})

	lowered := readSetting()
	lowered.Spec.Value = "1"
	if err := c.Update(ctx, &lowered); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if value := readSetting().Spec.Value; value != "1" {
		t.Fatalf("the ratchet must not fight a deliberate lowering, got %q", value)
	}
}
