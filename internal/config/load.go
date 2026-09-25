package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const (
	defaultPodCIDR     = "10.16.0.0/16"
	defaultServiceCIDR = "10.96.0.0/12"
	defaultJoinCIDR    = "100.64.0.0/16"
)

func Load(path string) (v1alpha1.ClusterConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return v1alpha1.ClusterConfig{}, err
	}
	var cfg v1alpha1.ClusterConfig
	if err := yaml.UnmarshalStrict(raw, &cfg); err != nil {
		return v1alpha1.ClusterConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg = withBundleBesides(WithDefaults(cfg), filepath.Dir(path))
	if err := Validate(cfg); err != nil {
		return v1alpha1.ClusterConfig{}, err
	}
	return cfg, nil
}

func withBundleBesides(cfg v1alpha1.ClusterConfig, dir string) v1alpha1.ClusterConfig {
	out := *cfg.DeepCopy()
	out.Spec.Registry.Bundle = resolvePath(dir, out.Spec.Registry.Bundle)
	return out
}

func resolvePath(dir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func WithDefaults(cfg v1alpha1.ClusterConfig) v1alpha1.ClusterConfig {
	out := *cfg.DeepCopy()
	out.Spec.API.VIPMode = fallback(out.Spec.API.VIPMode, "arp")
	out.Spec.Platform.TLSMode = fallback(out.Spec.Platform.TLSMode, "SelfSigned")
	out.Spec.Network.Fabric.PodCIDR = fallback(out.Spec.Network.Fabric.PodCIDR, defaultPodCIDR)
	out.Spec.Network.Fabric.ServiceCIDR = fallback(out.Spec.Network.Fabric.ServiceCIDR, defaultServiceCIDR)
	out.Spec.Network.Fabric.JoinCIDR = fallback(out.Spec.Network.Fabric.JoinCIDR, defaultJoinCIDR)
	out.Spec.Network.Fabric.EIPMode = fallback(out.Spec.Network.Fabric.EIPMode, "l2")
	if out.Spec.Storage.Replicas == 0 {
		out.Spec.Storage.Replicas = 1
	}
	if out.Spec.Addons == nil {
		out.Spec.Addons = map[string]bool{}
	}
	if _, ok := out.Spec.Addons["kata"]; !ok {
		out.Spec.Addons["kata"] = true
	}
	if _, ok := out.Spec.Addons["loki"]; !ok {
		out.Spec.Addons["loki"] = false
	}
	return out
}

func fallback(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

func Validate(cfg v1alpha1.ClusterConfig) error {
	if cfg.APIVersion != v1alpha1.GroupVersion.String() {
		return fmt.Errorf("apiVersion must be %s", v1alpha1.GroupVersion.String())
	}
	if cfg.Kind != "ClusterConfig" {
		return fmt.Errorf("kind must be ClusterConfig")
	}
	s := cfg.Spec
	if s.Version == "" {
		return fmt.Errorf("spec.version is required")
	}
	if _, err := netip.ParseAddr(s.API.VIP); err != nil {
		return fmt.Errorf("spec.api.vip: %w", err)
	}
	if !slices.Contains([]string{"arp", "bgp"}, s.API.VIPMode) {
		return fmt.Errorf("spec.api.vipMode must be arp or bgp")
	}
	if s.Network.ManagementInterface == "" {
		return fmt.Errorf("spec.network.managementInterface is required")
	}
	for _, cidr := range []string{s.Network.Fabric.PodCIDR, s.Network.Fabric.ServiceCIDR, s.Network.Fabric.JoinCIDR} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("spec.network.fabric cidr %q: %w", cidr, err)
		}
	}
	if s.Network.Fabric.PodCIDR != defaultPodCIDR || s.Network.Fabric.ServiceCIDR != defaultServiceCIDR || s.Network.Fabric.JoinCIDR != defaultJoinCIDR {
		return fmt.Errorf("custom CIDRs arrive in a later release")
	}
	if !slices.Contains([]string{"bgp", "l2"}, s.Network.Fabric.EIPMode) {
		return fmt.Errorf("spec.network.fabric.eipMode must be bgp or l2")
	}
	for _, pn := range s.Network.Fabric.ProviderNetworks {
		if _, _, err := net.ParseCIDR(pn.CIDR); err != nil {
			return fmt.Errorf("provider network %s cidr: %w", pn.Name, err)
		}
		if _, err := netip.ParseAddr(pn.Gateway); err != nil {
			return fmt.Errorf("provider network %s gateway: %w", pn.Name, err)
		}
	}
	if s.Storage.Replicas < 1 {
		return fmt.Errorf("spec.storage.replicas must be at least 1")
	}
	if len(s.Roles) == 0 {
		return fmt.Errorf("spec.roles needs at least one role")
	}
	for _, role := range s.Roles {
		if !v1alpha1.ValidRole(role) {
			return fmt.Errorf("unknown role %q", role)
		}
	}
	if !slices.Contains(s.Roles, v1alpha1.RoleControlPlane) {
		return fmt.Errorf("the first node must carry the control-plane role")
	}
	if !slices.Contains(s.Roles, v1alpha1.RoleCephOSD) {
		return fmt.Errorf("the first node must carry the ceph-osd role")
	}
	return nil
}

func ToCluster(cfg v1alpha1.ClusterConfig) v1alpha1.Cluster {
	return v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName, Labels: map[string]string{v1alpha1.LabelKind: "Cluster", v1alpha1.LabelName: v1alpha1.ClusterName}},
		Spec: v1alpha1.ClusterSpec{
			DesiredVersion:  cfg.Spec.Version,
			API:             cfg.Spec.API,
			NodeConcurrency: 1,
			Registry:        v1alpha1.RegistrySpec{Mirror: cfg.Spec.Registry.Mirror},
		},
	}
}

func ToHost(cfg v1alpha1.ClusterConfig, nodeName string) v1alpha1.Host {
	return v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{v1alpha1.LabelKind: "Host", v1alpha1.LabelName: nodeName}},
		Spec: v1alpha1.HostSpec{
			Roles:             slices.Clone(cfg.Spec.Roles),
			Management:        v1alpha1.ManagementSpec{Enabled: cfg.Spec.Host.Management},
			MaintenanceWindow: cfg.Spec.Host.MaintenanceWindow,
			Storage:           v1alpha1.HostStorageSpec{Devices: slices.Clone(cfg.Spec.Storage.Devices)},
		},
	}
}

func ToSettings(cfg v1alpha1.ClusterConfig) []v1alpha1.Setting {
	values := map[string]string{
		"platform.host":     cfg.Spec.Platform.Host,
		"platform.tls-mode": cfg.Spec.Platform.TLSMode,
		"storage.replicas":  strconv.Itoa(int(cfg.Spec.Storage.Replicas)),
		"storage.network":   vlanValue(cfg.Spec.Network.StorageVlan),
		"migration.network": vlanValue(cfg.Spec.Network.MigrationVlan),
		"virt.emulation":    strconv.FormatBool(cfg.Spec.Virtualization.Emulation),
		"kata.enabled":      strconv.FormatBool(cfg.Spec.Addons["kata"]),
		"loki.enabled":      strconv.FormatBool(cfg.Spec.Addons["loki"]),
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]v1alpha1.Setting, 0, len(keys))
	for _, key := range keys {
		out = append(out, v1alpha1.Setting{
			ObjectMeta: metav1.ObjectMeta{Name: key, Labels: map[string]string{v1alpha1.LabelKind: "Setting", v1alpha1.LabelName: key}},
			Spec:       v1alpha1.SettingSpec{Value: values[key]},
		})
	}
	return out
}

func vlanValue(vlan int32) string {
	if vlan == 0 {
		return ""
	}
	return strconv.Itoa(int(vlan))
}
