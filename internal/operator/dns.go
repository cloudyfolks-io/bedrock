package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	dnsGroup        = "coredns"
	dnsNamespace    = "kube-system"
	dnsName         = "coredns"
	keyDNSUpstreams = "dns.upstreams"
)

var dnsAddon = Addon{Name: "dns", Condition: v1alpha1.ConditionDNSReady, Render: RenderDNS}

func RenderDNS(in AddonInput) (Rendered, error) {
	if err := validateSettings(in.Settings, keyDNSUpstreams); err != nil {
		return Rendered{}, err
	}
	config, found, err := bundleObject(in.Bundle, dnsGroup, configMapGVK, client.ObjectKey{Namespace: dnsNamespace, Name: dnsName}, release.SettingVars(in.Settings, in.Cluster.Spec.API.VIP))
	if err != nil {
		return Rendered{}, err
	}
	if !found {
		return Rendered{SkipReason: "DNSNotInRelease", SkipMessage: fmt.Sprintf("ConfigMap %s/%s is not in the release", dnsNamespace, dnsName)}, nil
	}
	probe := Probe{GVK: deploymentGVK, Key: client.ObjectKey{Namespace: dnsNamespace, Name: dnsName}, Gate: release.DefaultGate}
	return Rendered{Objects: []*unstructured.Unstructured{config}, Probes: []Probe{probe}}, nil
}
