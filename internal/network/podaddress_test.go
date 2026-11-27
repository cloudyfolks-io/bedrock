package network

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodVPCAddressNeverReturnsTheManagementPodIP(t *testing.T) {
	for _, test := range []struct {
		annotations map[string]string
		want        string
	}{
		{nil, ""},
		{map[string]string{"k8s.v1.cni.cncf.io/network-status": `[{"interface":"eth0","ips":["100.72.0.8"]},{"interface":"net1","ips":["10.20.0.7/24"]}]`}, "10.20.0.7"},
		{map[string]string{"vpc.kube-system.fabric.cloudyfolks.io/allocated": "true", "vpc.kube-system.fabric.cloudyfolks.io/ip_address": "10.30.0.9"}, "10.30.0.9"},
		{map[string]string{"vpc.kube-system.fabric.cloudyfolks.io/ip_address": "10.30.0.9"}, ""},
	} {
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: test.annotations}, Status: corev1.PodStatus{PodIP: "100.72.0.8"}}
		if got := PodVPCAddress(pod); got != test.want {
			t.Fatalf("address %q, want %q", got, test.want)
		}
	}
}
