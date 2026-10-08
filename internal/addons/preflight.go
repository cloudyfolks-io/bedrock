package addons

import (
	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type VerdictState string

const (
	VerdictPassed  VerdictState = "Passed"
	VerdictFailed  VerdictState = "Failed"
	VerdictUnknown VerdictState = "Unknown"
)

type Verdict struct {
	State   VerdictState
	Reason  string
	Message string
}

func KataPreflight(hosts []v1alpha1.Host) Verdict {
	waiting := false
	for _, host := range hosts {
		if !v1alpha1.HostHasRole(host, v1alpha1.RoleWorkload) {
			continue
		}
		if host.Status.Checks == nil || host.Status.Checks.KVM == nil {
			waiting = true
			continue
		}
		if *host.Status.Checks.KVM {
			return Verdict{State: VerdictPassed, Reason: "Passed", Message: "a workload host has /dev/kvm"}
		}
	}
	if waiting || len(hosts) == 0 {
		return Verdict{State: VerdictUnknown, Reason: "WaitingForHosts", Message: "a workload host has not reported KVM yet"}
	}
	return Verdict{State: VerdictFailed, Reason: "NoKVM", Message: "no workload host has /dev/kvm; kata needs bare-metal KVM or nested virtualization"}
}
