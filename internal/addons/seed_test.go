package addons

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func resetAddonsAndSettings(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	var addons v1alpha1.AddonList
	if err := c.List(ctx, &addons); err != nil {
		t.Fatal(err)
	}
	for i := range addons.Items {
		addon := &addons.Items[i]
		addon.Finalizers = nil
		_ = c.Update(ctx, addon)
		_ = c.Delete(ctx, addon)
	}
	_ = c.DeleteAllOf(ctx, &v1alpha1.Setting{})
}

func newSeedClient(t *testing.T) client.Client {
	t.Helper()
	c := newClient(t)
	resetAddonsAndSettings(t, c)
	t.Cleanup(func() { resetAddonsAndSettings(t, c) })
	return c
}

func getAddon(t *testing.T, c client.Client, name string) v1alpha1.Addon {
	t.Helper()
	var addon v1alpha1.Addon
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &addon); err != nil {
		t.Fatal(err)
	}
	return addon
}

func setSetting(t *testing.T, c client.Client, key, value string) {
	t.Helper()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}, Spec: v1alpha1.SettingSpec{Value: value}}
	if err := c.Create(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
}

func TestSeedCreatesKnownAddons(t *testing.T) {
	c := newSeedClient(t)
	if err := Seed(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !getAddon(t, c, "kata").Spec.Enabled || getAddon(t, c, "loki").Spec.Enabled || getAddon(t, c, "clusters").Spec.Enabled {
		t.Fatal("defaults are kata on, loki and clusters off")
	}
	if getAddon(t, c, "kata").Labels["bedrock.cloudyfolks.io/kind"] != "Addon" {
		t.Fatal("kind label missing")
	}
}

func TestSeedReadsTheInstallSettings(t *testing.T) {
	c := newSeedClient(t)
	setSetting(t, c, "kata.enabled", "false")
	setSetting(t, c, "loki.enabled", "true")
	if err := Seed(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if getAddon(t, c, "kata").Spec.Enabled || !getAddon(t, c, "loki").Spec.Enabled {
		t.Fatal("the settings were not read")
	}
}

func TestSeedNeverOverwritesUserValues(t *testing.T) {
	c := newSeedClient(t)
	ctx := context.Background()
	addon := &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "loki"}, Spec: v1alpha1.AddonSpec{Enabled: true}}
	addon.Spec.Values = jsonOf([]byte(`{"persistence.size":"5Gi"}`))
	if err := c.Create(ctx, addon); err != nil {
		t.Fatal(err)
	}
	before := getAddon(t, c, "loki")
	for range 2 {
		if err := Seed(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	after := getAddon(t, c, "loki")
	if !after.Spec.Enabled || after.Spec.Values == nil || string(after.Spec.Values.Raw) != string(before.Spec.Values.Raw) || after.ResourceVersion != before.ResourceVersion {
		t.Fatalf("addon changed: %+v", after)
	}
}

func TestSeedKeepsADisabledKata(t *testing.T) {
	c := newSeedClient(t)
	if err := c.Create(context.Background(), &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "kata"}}); err != nil {
		t.Fatal(err)
	}
	if err := Seed(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if getAddon(t, c, "kata").Spec.Enabled {
		t.Fatal("kata was enabled")
	}
}
