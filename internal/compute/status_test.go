package compute

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func TestPhase(t *testing.T) {
	cases := []struct {
		status  string
		running bool
		want    string
	}{
		{"Running", true, "Running"}, {"Running", false, "Running"}, {"Paused", true, "Running"},
		{"Migrating", true, "Migrating"}, {"Stopped", true, "Starting"}, {"Stopped", false, "Stopped"},
		{"Starting", true, "Starting"}, {"Provisioning", true, "Starting"}, {"WaitingForVolumeBinding", true, "Starting"},
		{"", true, "Starting"}, {"", false, "Stopped"}, {"Stopping", false, "Stopping"}, {"Terminating", false, "Stopping"},
		{"ErrorUnschedulable", true, "Failed"}, {"CrashLoopBackOff", true, "Failed"}, {"ErrImagePull", true, "Failed"},
		{"ImagePullBackOff", true, "Failed"}, {"ErrorPvcNotFound", true, "Failed"}, {"DataVolumeError", true, "Failed"},
		{"Unknown", true, "Failed"}, {"SomethingNew", true, "Failed"},
	}
	for _, tc := range cases {
		if got := Phase(tc.status, tc.running); got != tc.want {
			t.Errorf("%q running=%v gave %q want %q", tc.status, tc.running, got, tc.want)
		}
	}
}

func TestConsolePaths(t *testing.T) {
	want := v1alpha1.VMConsole{
		VNCPath:    "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/web-1/console/vnc",
		SerialPath: "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/virtualmachines/web-1/console/serial",
	}
	if got := ConsolePaths("acme-web", "web-1"); got != want {
		t.Fatalf("%+v", got)
	}
	for _, phase := range []string{"Running", "Migrating"} {
		if got := ConsoleFor(phase, "acme-web", "web-1"); got == nil || *got != want {
			t.Errorf("%s: %+v", phase, got)
		}
	}
	for _, phase := range []string{"Stopped", "Starting", "Stopping", "Failed"} {
		if ConsoleFor(phase, "acme-web", "web-1") != nil {
			t.Errorf("%s must have no console", phase)
		}
	}
}

func launcherPod(node string, annotations map[string]string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "launcher-" + node, Annotations: annotations}, Spec: corev1.PodSpec{NodeName: node}}
}

func addressAnnotations(subnet, ip, mac string) map[string]string {
	prefix := network.SubnetProvider("acme-web", subnet) + ".cloudyfolks.io/"
	out := map[string]string{prefix + "ip_address": ip}
	if mac != "" {
		out[prefix+"mac_address"] = mac
	}
	return out
}

func TestAddressesOf(t *testing.T) {
	pod := launcherPod("node-1", addressAnnotations("default", "10.100.0.7", "52:54:00:aa:bb:cc"))
	got := AddressesOf([]corev1.Pod{pod}, "node-1", "acme-web", []string{"default", "db"})
	if len(got) != 1 || got[0] != (v1alpha1.VMAddress{Subnet: "default", IP: "10.100.0.7", MAC: "52:54:00:aa:bb:cc"}) {
		t.Fatalf("%+v", got)
	}
	dual := launcherPod("node-1", addressAnnotations("default", "10.100.0.7,fd00::7", "52:54:00:aa:bb:cc"))
	got = AddressesOf([]corev1.Pod{dual}, "node-1", "acme-web", []string{"default"})
	if len(got) != 2 || got[1].IP != "fd00::7" || got[1].MAC != "52:54:00:aa:bb:cc" || got[1].Subnet != "default" {
		t.Fatalf("%+v", got)
	}
	source := launcherPod("node-1", addressAnnotations("default", "10.100.0.7", ""))
	target := launcherPod("node-2", addressAnnotations("default", "10.100.0.9", ""))
	got = AddressesOf([]corev1.Pod{source, target}, "node-2", "acme-web", []string{"default"})
	if len(got) != 1 || got[0].IP != "10.100.0.9" {
		t.Fatalf("%+v", got)
	}
	now := metav1.NewTime(time.Now())
	dying := launcherPod("node-2", addressAnnotations("default", "10.100.0.9", ""))
	dying.DeletionTimestamp = &now
	got = AddressesOf([]corev1.Pod{dying, source}, "node-2", "acme-web", []string{"default"})
	if len(got) != 1 || got[0].IP != "10.100.0.7" {
		t.Fatalf("%+v", got)
	}
	if AddressesOf(nil, "node-1", "acme-web", []string{"default"}) != nil {
		t.Fatal("no pod, no address")
	}
}

func TestGuestAgentFrom(t *testing.T) {
	got, err := GuestAgentFrom([]byte(`{"guestAgentVersion":"5.0.0","hostname":"web-1","os":{"prettyName":"Ubuntu 24.04 LTS"}}`))
	if err != nil || got != (v1alpha1.VMGuestAgent{Version: "5.0.0", OS: "Ubuntu 24.04 LTS", Hostname: "web-1"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := GuestAgentFrom([]byte("{")); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func condition(conditions []metav1.Condition, name string) *metav1.Condition {
	return meta.FindStatusCondition(conditions, name)
}

func TestConditions(t *testing.T) {
	ready := func(phase string, running bool, gate *Gate, observed ObservedVM) *metav1.Condition {
		return condition(Conditions(7, phase, running, gate, observed), v1alpha1.ConditionReady)
	}
	for phase, want := range map[string]struct {
		status metav1.ConditionStatus
		reason string
	}{
		"Running": {metav1.ConditionTrue, "Running"}, "Migrating": {metav1.ConditionTrue, "Migrating"},
		"Stopped": {metav1.ConditionTrue, "Stopped"}, "Starting": {metav1.ConditionFalse, "Starting"},
		"Stopping": {metav1.ConditionFalse, "Stopping"}, "Failed": {metav1.ConditionFalse, "Failed"},
	} {
		got := ready(phase, phase != "Stopped", nil, ObservedVM{Message: "boom"})
		if got.Status != want.status || got.Reason != want.reason || got.ObservedGeneration != 7 {
			t.Errorf("%s: %+v", phase, got)
		}
	}
	if got := ready("Failed", true, nil, ObservedVM{Message: "boom"}); got.Message != "the virtual machine failed: boom" {
		t.Errorf("message %q", got.Message)
	}
	if got := condition(Conditions(1, "Running", true, nil, ObservedVM{}), ConditionNetworkReady); got == nil || got.Status != metav1.ConditionTrue {
		t.Errorf("network ready %+v", got)
	}
	secGate := &Gate{Reason: ReasonSecurityGroupNotReady, Message: "no group"}
	got := Conditions(1, "Running", true, secGate, ObservedVM{})
	if r, n := condition(got, v1alpha1.ConditionReady), condition(got, ConditionNetworkReady); r.Status != metav1.ConditionFalse || r.Reason != ReasonSecurityGroupNotReady || n.Status != metav1.ConditionFalse || n.Reason != ReasonSecurityGroupNotReady {
		t.Errorf("%+v %+v", r, n)
	}
	got = Conditions(1, "Running", true, &Gate{Reason: ReasonImageNotReady}, ObservedVM{})
	if condition(got, ConditionNetworkReady) != nil || condition(got, v1alpha1.ConditionReady).Reason != ReasonImageNotReady {
		t.Errorf("%+v", got)
	}
	got = Conditions(1, "Running", true, &Gate{Reason: ReasonSSHKeyNotReady}, ObservedVM{})
	if n := condition(got, ConditionNetworkReady); n == nil || n.Status != metav1.ConditionTrue || condition(got, v1alpha1.ConditionReady).Status != metav1.ConditionFalse {
		t.Errorf("%+v", got)
	}
}

func TestConditionsFromKubeVirt(t *testing.T) {
	absent := Conditions(1, "Running", true, nil, ObservedVM{})
	if condition(absent, ConditionGuestAgent) != nil || condition(absent, ConditionLiveMigratable) != nil || condition(absent, ConditionRestartRequired) != nil {
		t.Fatalf("%+v", absent)
	}
	observed := ObservedVM{
		VMIConditions: map[string]string{"AgentConnected": "True", "LiveMigratable": "False"},
		VMConditions:  map[string]string{"RestartRequired": "True"},
	}
	got := Conditions(3, "Running", true, nil, observed)
	if a := condition(got, ConditionGuestAgent); a.Status != metav1.ConditionTrue || a.ObservedGeneration != 3 {
		t.Errorf("%+v", a)
	}
	if m := condition(got, ConditionLiveMigratable); m.Status != metav1.ConditionFalse {
		t.Errorf("%+v", m)
	}
	if r := condition(got, ConditionRestartRequired); r == nil || r.Status != metav1.ConditionTrue {
		t.Errorf("%+v", r)
	}
	observed.VMConditions["RestartRequired"] = "False"
	if condition(Conditions(3, "Running", true, nil, observed), ConditionRestartRequired) != nil {
		t.Error("RestartRequired appears only when true")
	}
	for index := 1; index < len(got); index++ {
		if got[index-1].Type > got[index].Type {
			t.Errorf("conditions are not sorted: %+v", got)
		}
	}
}
