package addons

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func testHost(role string, kvm *bool, reported bool) v1alpha1.Host {
	host := v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: "h"}, Spec: v1alpha1.HostSpec{Roles: []string{role}}}
	if reported {
		host.Status.Checks = &v1alpha1.HostChecks{KVM: kvm}
	}
	return host
}

func TestKataPreflight(t *testing.T) {
	yes, no := true, false
	workload := v1alpha1.RoleWorkload
	cases := []struct {
		name  string
		hosts []v1alpha1.Host
		want  VerdictState
	}{
		{"no hosts", nil, VerdictUnknown},
		{"kvm host", []v1alpha1.Host{testHost(workload, &yes, true)}, VerdictPassed},
		{"no kvm host", []v1alpha1.Host{testHost(workload, &no, true)}, VerdictFailed},
		{"nil checks", []v1alpha1.Host{testHost(workload, nil, false)}, VerdictUnknown},
		{"nil fact", []v1alpha1.Host{testHost(workload, nil, true)}, VerdictUnknown},
		{"true and nil", []v1alpha1.Host{testHost(workload, nil, false), testHost(workload, &yes, true)}, VerdictPassed},
		{"false and nil", []v1alpha1.Host{testHost(workload, &no, true), testHost(workload, nil, false)}, VerdictUnknown},
		{"control plane only", []v1alpha1.Host{testHost(v1alpha1.RoleControlPlane, &yes, true)}, VerdictFailed},
	}
	for _, c := range cases {
		got := KataPreflight(c.hosts)
		if got.State != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got.State, c.want)
		}
	}
	if got := KataPreflight([]v1alpha1.Host{testHost(workload, &no, true)}); got.Reason != "NoKVM" {
		t.Errorf("reason %q", got.Reason)
	}
	if got := KataPreflight(nil); got.Reason != "WaitingForHosts" {
		t.Errorf("reason %q", got.Reason)
	}
}
