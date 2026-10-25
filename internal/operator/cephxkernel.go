package operator

import (
	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/cephx"
)

const cephxCipherKey = "storage.cephx-cipher"

func cephxKernelProblems(cipher string, hosts []v1alpha1.Host) []string {
	if cipher != cephx.Aes256k {
		return nil
	}
	var problems []string
	for _, host := range hosts {
		if problem := cephx.KernelProblem(host.Name, host.Status.Inventory.Firmware.Kernel); problem != "" {
			problems = append(problems, problem)
		}
	}
	return problems
}
