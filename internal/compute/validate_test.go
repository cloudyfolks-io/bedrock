package compute

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func validFixture() v1alpha1.VirtualMachine {
	return fixtureVM("acme-web", "web-1")
}

func issueFields(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue.Field)
		if issue.Reason != ReasonInvalidSpec {
			out = append(out, "wrong reason "+issue.Reason)
		}
	}
	return out
}

func hasField(issues []Issue, field string) bool {
	for _, issue := range issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}

func TestValidateAcceptsTheFixture(t *testing.T) {
	if issues := ValidateVirtualMachine(validFixture()); len(issues) != 0 {
		t.Fatalf("%+v", issues)
	}
}

func TestValidateRefusesSeparatorsInReferences(t *testing.T) {
	bad := []string{"other/default", "a,b", `{"x":1}`, "a b", "Default", ""}
	for _, value := range bad {
		cases := map[string]struct {
			mutate func(*v1alpha1.VirtualMachine)
			field  string
		}{
			"subnet":         {func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SubnetRef = value }, "spec.networks[0].subnetRef"},
			"security group": {func(vm *v1alpha1.VirtualMachine) { vm.Spec.Networks[0].SecurityGroups = []string{value} }, "spec.networks[0].securityGroups[0]"},
			"volume":         {func(vm *v1alpha1.VirtualMachine) { vm.Spec.Disks[0].VolumeRef = value }, "spec.disks[0].volumeRef"},
			"ssh key":        {func(vm *v1alpha1.VirtualMachine) { vm.Spec.SSHKeys = []string{value} }, "spec.sshKeys[0]"},
			"cdrom":          {func(vm *v1alpha1.VirtualMachine) { vm.Spec.CDROMs = []v1alpha1.VMCDROM{{ImageRef: value}} }, "spec.cdroms[0].imageRef"},
			"image":          {func(vm *v1alpha1.VirtualMachine) { vm.Spec.Image.Name = value }, "spec.image.name"},
		}
		for label, tc := range cases {
			vm := validFixture()
			tc.mutate(&vm)
			if issues := ValidateVirtualMachine(vm); !hasField(issues, tc.field) {
				t.Errorf("%s %q: %+v", label, value, issues)
			}
		}
		if value == "" {
			continue
		}
		vm := validFixture()
		vm.Spec.Networks[0].ElasticIPRef = value
		if !hasField(ValidateVirtualMachine(vm), "spec.networks[0].elasticIPRef") {
			t.Errorf("elastic ip %q", value)
		}
		vm = validFixture()
		vm.Spec.CloudInit = &v1alpha1.VMCloudInit{SecretRef: value}
		if !hasField(ValidateVirtualMachine(vm), "spec.cloudInit.secretRef") {
			t.Errorf("secret %q", value)
		}
	}
}

func TestValidateRefusesDuplicateSubnets(t *testing.T) {
	vm := validFixture()
	vm.Spec.Networks = []v1alpha1.VMNetwork{{SubnetRef: "a"}, {SubnetRef: "a"}}
	if !hasField(ValidateVirtualMachine(vm), "spec.networks[1].subnetRef") {
		t.Fatal("a duplicate subnet must be refused")
	}
}

func TestValidateRefusesDuplicateVolumes(t *testing.T) {
	vm := validFixture()
	vm.Spec.Disks = []v1alpha1.VMDisk{{VolumeRef: "a"}, {VolumeRef: "a"}}
	if !hasField(ValidateVirtualMachine(vm), "spec.disks[1].volumeRef") {
		t.Fatal("a duplicate volume must be refused")
	}
}

func TestValidateFirmware(t *testing.T) {
	vm := validFixture()
	vm.Spec.Firmware = &v1alpha1.VMFirmware{SecureBoot: true}
	if !hasField(ValidateVirtualMachine(vm), "spec.firmware.secureBoot") {
		t.Fatal("secureBoot without uefi must be refused")
	}
	vm.Spec.Firmware = &v1alpha1.VMFirmware{SecureBoot: true, UEFI: true}
	if issues := ValidateVirtualMachine(vm); len(issues) != 0 {
		t.Fatalf("%+v", issues)
	}
}

func TestValidateCloudInitMix(t *testing.T) {
	vm := validFixture()
	vm.Spec.CloudInit = &v1alpha1.VMCloudInit{SecretRef: "s", UserData: "x"}
	if !hasField(ValidateVirtualMachine(vm), "spec.cloudInit.secretRef") {
		t.Fatal("secretRef with inline data must be refused")
	}
	vm.Spec.CloudInit = &v1alpha1.VMCloudInit{UserData: "x", NetworkData: "y"}
	if issues := ValidateVirtualMachine(vm); len(issues) != 0 {
		t.Fatalf("%+v", issues)
	}
}

func TestValidateVMName(t *testing.T) {
	for _, name := range []string{strings.Repeat("a", 53), "web.one"} {
		vm := fixtureVM("acme-web", name)
		if !hasField(ValidateVirtualMachine(vm), "metadata.name") {
			t.Errorf("name %q", name)
		}
	}
}

func TestValidateSchedulingKeys(t *testing.T) {
	for _, selector := range []map[string]string{{"bad key": "x"}, {"disk": strings.Repeat("a", 64)}} {
		vm := validFixture()
		vm.Spec.Scheduling = &v1alpha1.VMScheduling{NodeSelector: selector}
		if len(ValidateVirtualMachine(vm)) == 0 {
			t.Errorf("selector %+v", selector)
		}
	}
}

func TestValidateReturnsEveryIssue(t *testing.T) {
	vm := validFixture()
	vm.Spec.Image.Name = "a/b"
	vm.Spec.Networks[0].SubnetRef = "Bad"
	vm.Spec.SSHKeys = []string{"a b"}
	fields := issueFields(ValidateVirtualMachine(vm))
	want := []string{"spec.image.name", "spec.networks[0].subnetRef", "spec.sshKeys[0]"}
	if len(fields) != len(want) {
		t.Fatalf("%v", fields)
	}
	for index := range want {
		if fields[index] != want[index] {
			t.Fatalf("%v", fields)
		}
	}
}

func affinityWithTerm(term corev1.PodAffinityTerm) []*corev1.Affinity {
	weighted := []corev1.WeightedPodAffinityTerm{{Weight: 1, PodAffinityTerm: term}}
	required := []corev1.PodAffinityTerm{term}
	return []*corev1.Affinity{
		{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: required}},
		{PodAffinity: &corev1.PodAffinity{PreferredDuringSchedulingIgnoredDuringExecution: weighted}},
		{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: required}},
		{PodAntiAffinity: &corev1.PodAntiAffinity{PreferredDuringSchedulingIgnoredDuringExecution: weighted}},
	}
}

func TestScopedAffinityRefusesOtherNamespaces(t *testing.T) {
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}
	for _, term := range []corev1.PodAffinityTerm{
		{LabelSelector: selector, TopologyKey: "kubernetes.io/hostname", Namespaces: []string{"other"}},
		{LabelSelector: selector, TopologyKey: "kubernetes.io/hostname", Namespaces: []string{"acme-web"}},
		{LabelSelector: selector, TopologyKey: "kubernetes.io/hostname", NamespaceSelector: &metav1.LabelSelector{}},
	} {
		for _, affinity := range affinityWithTerm(term) {
			if _, err := ScopedAffinity(affinity); err == nil {
				t.Errorf("accepted %+v", affinity)
			}
		}
	}
}

func TestScopedAffinityAllowsOwnTerms(t *testing.T) {
	term := corev1.PodAffinityTerm{LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}, TopologyKey: "kubernetes.io/hostname"}
	inputs := append(affinityWithTerm(term), &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}})
	for _, in := range inputs {
		before := in.DeepCopy()
		out, err := ScopedAffinity(in)
		if err != nil || out == in {
			t.Fatalf("%v same pointer %v", err, out == in)
		}
		out.NodeAffinity = &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{}}
		if !reflect.DeepEqual(in, before) {
			t.Fatal("the input changed")
		}
	}
	if out, err := ScopedAffinity(nil); out != nil || err != nil {
		t.Fatalf("nil gave %v %v", out, err)
	}
}
