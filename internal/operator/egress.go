package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	certManagerGroup   = "cert-manager"
	certManagerNS      = "cert-manager"
	certManagerName    = "cert-manager"
	keyEgressProxy     = "egress.http-proxy"
	keyEgressNoProxy   = "egress.no-proxy"
	keyDNS01Nameserver = "letsencrypt.dns01-nameservers"
)

var egressAddon = Addon{Name: "egress", Condition: v1alpha1.ConditionEgressReady, Render: RenderEgress}

func RenderEgress(in AddonInput) (Rendered, error) {
	if err := validateSettings(in.Settings, keyEgressProxy, keyEgressNoProxy, keyDNS01Nameserver); err != nil {
		return Rendered{}, err
	}
	key := client.ObjectKey{Namespace: certManagerNS, Name: certManagerName}
	controller, found, err := bundleObject(in.Bundle, certManagerGroup, deploymentGVK, key, release.SettingVars(in.Settings, in.Cluster.Spec.API.VIP))
	if err != nil {
		return Rendered{}, err
	}
	if !found {
		return Rendered{SkipReason: "CertManagerNotInRelease", SkipMessage: fmt.Sprintf("Deployment %s/%s is not in the release", certManagerNS, certManagerName)}, nil
	}
	probe := Probe{GVK: deploymentGVK, Key: key, Gate: release.DefaultGate}
	return Rendered{Objects: []*unstructured.Unstructured{controller}, Probes: []Probe{probe}}, nil
}
