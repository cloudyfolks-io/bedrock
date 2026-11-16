package network

import (
	"encoding/json"
	"net/netip"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

func PodVPCAddress(pod corev1.Pod) string {
	prefix := vpcAttachmentProvider + providerAnnotationDomain
	if pod.Annotations[prefix+"allocated"] == "true" {
		for _, value := range strings.Split(pod.Annotations[prefix+addressAnnotation], ",") {
			if address, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
				return address.String()
			}
		}
	}
	var statuses []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
	}
	if err := json.Unmarshal([]byte(pod.Annotations["k8s.v1.cni.cncf.io/network-status"]), &statuses); err != nil {
		return ""
	}
	for _, status := range statuses {
		if status.Interface != "net1" {
			continue
		}
		for _, value := range status.IPs {
			if address, err := netip.ParseAddr(value); err == nil {
				return address.String()
			}
			if prefix, err := netip.ParsePrefix(value); err == nil {
				return prefix.Addr().String()
			}
		}
	}
	return ""
}
