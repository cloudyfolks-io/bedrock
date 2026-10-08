package addons

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestHostEventsPassOnlyForPreflightInputs(t *testing.T) {
	yes, no := true, false
	base := v1alpha1.Host{Spec: v1alpha1.HostSpec{Roles: []string{"workload"}}}
	withKVM := func(kvm *bool) *v1alpha1.Host {
		host := base.DeepCopy()
		host.Status.Checks = &v1alpha1.HostChecks{KVM: kvm, VarLibFreeBytes: 1}
		return host
	}
	busier := withKVM(&yes)
	busier.Status.Checks.VarLibFreeBytes = 2
	rolesChanged := withKVM(&yes)
	rolesChanged.Spec.Roles = []string{"workload", "control-plane"}
	cases := []struct {
		name     string
		old, new *v1alpha1.Host
		want     bool
	}{
		{"free space only", withKVM(&yes), busier, false},
		{"kvm appears", &base, withKVM(&yes), true},
		{"kvm flips", withKVM(&yes), withKVM(&no), true},
		{"kvm unchanged", withKVM(&no), withKVM(&no), false},
		{"roles change", withKVM(&yes), rolesChanged, true},
	}
	for _, c := range cases {
		if got := hostPreflightInputsChanged(c.old, c.new); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if hostKVMChanged(withKVM(&yes), rolesChanged) {
		t.Error("a role change is not a kvm change")
	}
}

func TestSettingEventsPassOnlyForTheSettingsTheAddonsRead(t *testing.T) {
	setting := func(name, value string) *v1alpha1.Setting {
		return &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.SettingSpec{Value: value}}
	}
	for _, name := range []string{"loki.retention-days", "kata.enabled", "loki.enabled"} {
		if !settingValueChanged(setting(name, "1"), setting(name, "2")) {
			t.Errorf("%s change ignored", name)
		}
		if settingValueChanged(setting(name, "1"), setting(name, "1")) {
			t.Errorf("%s status-only change passed", name)
		}
	}
	if settingValueChanged(setting("images.keep", "1"), setting("images.keep", "2")) {
		t.Error("an unrelated setting passed")
	}
	if readSetting(setting("audit.level", "")) {
		t.Error("audit.level is read by the observability controller, not the addon controller")
	}
}

func TestNodeEventsPassOnlyForTheKVMLabel(t *testing.T) {
	node := func(labels map[string]string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: labels}}
	}
	if nodeKVMLabelChanged(node(map[string]string{"a": "1"}), node(map[string]string{"a": "2"})) {
		t.Error("another label passed")
	}
	if !nodeKVMLabelChanged(node(map[string]string{LabelKVM: "true"}), node(nil)) {
		t.Error("a removed kvm label was ignored")
	}
	if !nodeKVMLabelChanged(node(nil), node(map[string]string{LabelKVM: "true"})) {
		t.Error("an added kvm label was ignored")
	}
}
