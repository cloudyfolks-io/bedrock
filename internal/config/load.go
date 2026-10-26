package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/cephx"
	"github.com/cloudyfolks-io/bedrock/internal/dnsname"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	DefaultPodCIDR     = "10.16.0.0/16"
	DefaultServiceCIDR = "10.96.0.0/12"
	DefaultJoinCIDR    = "100.64.0.0/16"

	adoptDevicePrefix = "/dev/disk/by-id/"
)

var (
	adoptFSID      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	adoptMonitorID = regexp.MustCompile(`^[a-z]+$`)
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
	out.Spec.Network.Fabric.PodCIDR = fallback(out.Spec.Network.Fabric.PodCIDR, DefaultPodCIDR)
	out.Spec.Network.Fabric.ServiceCIDR = fallback(out.Spec.Network.Fabric.ServiceCIDR, DefaultServiceCIDR)
	out.Spec.Network.Fabric.JoinCIDR = fallback(out.Spec.Network.Fabric.JoinCIDR, DefaultJoinCIDR)
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
	if _, ok := out.Spec.Addons["clusters"]; !ok {
		out.Spec.Addons["clusters"] = false
	}
	if _, ok := out.Spec.Addons["descheduler"]; !ok {
		out.Spec.Addons["descheduler"] = false
	}
	return out
}

func WithCephxCipher(cfg v1alpha1.ClusterConfig, kernel string) v1alpha1.ClusterConfig {
	out := *cfg.DeepCopy()
	if out.Spec.Storage.CephxCipher == "" {
		out.Spec.Storage.CephxCipher = pickCephxCipher(out.Spec.Storage.Adopt, kernel)
	}
	return out
}

func pickCephxCipher(adopt *v1alpha1.AdoptSpec, kernel string) string {
	if adopt == nil {
		return cephx.ForKernel(kernel)
	}
	return adoptedCephxCipher(adopt.Cephx)
}

func adoptedCephxCipher(raw *runtime.RawExtension) string {
	object, err := cephxObject(raw)
	if err != nil || object == nil {
		return cephx.Aes
	}
	if csi, _ := object["csi"].(map[string]any); csi["keyType"] == cephx.Aes256k {
		return cephx.Aes256k
	}
	return cephx.Aes
}

func cephxObject(raw *runtime.RawExtension) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw.Raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("storage.adopt.cephx: not a JSON object")
	}
	return object, nil
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
	if err := validatePlatformHost(s.Platform.Host); err != nil {
		return err
	}
	if err := validateKMSEndpoint(s.Encryption.KMS); err != nil {
		return err
	}
	if s.Network.ManagementInterface == "" {
		return fmt.Errorf("spec.network.managementInterface is required")
	}
	for _, cidr := range []string{s.Network.Fabric.PodCIDR, s.Network.Fabric.ServiceCIDR, s.Network.Fabric.JoinCIDR} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("spec.network.fabric cidr %q: %w", cidr, err)
		}
	}
	if s.Network.Fabric.PodCIDR != DefaultPodCIDR || s.Network.Fabric.ServiceCIDR != DefaultServiceCIDR || s.Network.Fabric.JoinCIDR != DefaultJoinCIDR {
		return fmt.Errorf("custom CIDRs arrive in a later release")
	}
	if mtu := s.Network.PodMTU; mtu != 0 && (mtu < 576 || mtu > 9000) {
		return fmt.Errorf("spec.network.podMTU must be from 576 to 9000, got %d", mtu)
	}
	if err := validateManagementNetwork(s.Network); err != nil {
		return err
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
	if err := settings.ValidateProviderNetworks(s.Network.Fabric.ProviderNetworks); err != nil {
		return fmt.Errorf("spec.network.fabric.providerNetworks: %w", err)
	}
	if err := validateClusterServices(cfg); err != nil {
		return err
	}
	if s.Storage.Replicas < 1 {
		return fmt.Errorf("spec.storage.replicas must be at least 1")
	}
	if cipher := s.Storage.CephxCipher; cipher != "" && cipher != cephx.Aes && cipher != cephx.Aes256k {
		return fmt.Errorf("spec.storage.cephxCipher must be %s or %s", cephx.Aes, cephx.Aes256k)
	}
	if err := validateAdopt(s.Storage); err != nil {
		return err
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

var legInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func validateManagementNetwork(network v1alpha1.ClusterNetworkSpec) error {
	if cidr := network.ManagementCIDR; cidr != "" {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("spec.network.managementCIDR: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, leg := range network.TenantLegs {
		if !legInterfacePattern.MatchString(leg.Interface) {
			return fmt.Errorf("spec.network.tenantLegs interface %q is not a valid interface name", leg.Interface)
		}
		if seen[leg.Interface] {
			return fmt.Errorf("spec.network.tenantLegs lists %s twice", leg.Interface)
		}
		seen[leg.Interface] = true
		for _, source := range leg.AllowSources {
			prefix, err := netip.ParsePrefix(source)
			if err != nil {
				return fmt.Errorf("spec.network.tenantLegs %s allowSources: %w", leg.Interface, err)
			}
			if prefix.Bits() == 0 {
				return fmt.Errorf("spec.network.tenantLegs %s allowSources: %s matches every address", leg.Interface, source)
			}
		}
	}
	return nil
}

func validateClusterServices(cfg v1alpha1.ClusterConfig) error {
	values := SettingValues(cfg)
	fields := []struct{ field, key string }{
		{"spec.dns.upstreams", "dns.upstreams"},
		{"spec.egress.httpProxy", "egress.http-proxy"},
		{"spec.egress.noProxy", "egress.no-proxy"},
		{"spec.egress.dns01Nameservers", "letsencrypt.dns01-nameservers"},
	}
	for _, f := range fields {
		definition, _ := settings.Lookup(f.key)
		if err := definition.Validate(values[f.key]); err != nil {
			return fmt.Errorf("%s: %w", f.field, err)
		}
	}
	return nil
}

func validateAdopt(storage v1alpha1.StorageSpec) error {
	adopt := storage.Adopt
	if adopt == nil {
		return nil
	}
	if len(storage.Devices) > 0 {
		return fmt.Errorf("storage.devices: must be empty when storage.adopt is set")
	}
	if !adoptFSID.MatchString(adopt.FSID) {
		return fmt.Errorf("storage.adopt.fsid: not a lowercase UUID")
	}
	if !filepath.IsAbs(adopt.KeyFile) {
		return fmt.Errorf("storage.adopt.keyFile: not an absolute path")
	}
	if message := v1alpha1.AdoptPoolNameProblem(adopt.Pools); message != "" {
		return errors.New(message)
	}
	if err := validateAdoptCephx(adopt.Cephx); err != nil {
		return err
	}
	if err := validateAdoptHosts(adopt.Hosts); err != nil {
		return err
	}
	if err := validateAdoptMonitors(*adopt); err != nil {
		return err
	}
	if len(adopt.Hosts) >= 3 && storage.Replicas < 3 {
		return fmt.Errorf("storage.replicas must be at least 3 to adopt")
	}
	return nil
}

var cephxKeyedSections = []string{"daemon", "csi", "rbdMirrorPeer"}

func validateAdoptCephx(raw *runtime.RawExtension) error {
	object, err := cephxObject(raw)
	if err != nil || object == nil {
		return err
	}
	rotated := adoptedCephxCipher(raw) == cephx.Aes256k
	for _, name := range cephxKeyedSections {
		section, present := object[name]
		if !present && !rotated {
			continue
		}
		fields, _ := section.(map[string]any)
		if _, ok := fields["keyGeneration"].(float64); !ok {
			return fmt.Errorf("storage.adopt.cephx.%s.keyGeneration: required, Rook refuses a lower generation than the old cluster has", name)
		}
	}
	return nil
}

func validateAdoptHosts(hosts []v1alpha1.AdoptHost) error {
	seenDevices := map[string]bool{}
	for i, host := range hosts {
		field := fmt.Sprintf("storage.adopt.hosts[%d]", i)
		if host.Node == "" {
			return fmt.Errorf("%s.node: empty", field)
		}
		if len(validation.IsDNS1123Label(host.Node)) > 0 {
			return fmt.Errorf("%s.node: not a DNS-1123 label", field)
		}
		if slices.ContainsFunc(hosts[:i], func(earlier v1alpha1.AdoptHost) bool { return earlier.Node == host.Node }) {
			return fmt.Errorf("%s.node: duplicate %q", field, host.Node)
		}
		for j, device := range host.Devices {
			if !strings.HasPrefix(device, adoptDevicePrefix) || filepath.Clean(device) != device {
				return fmt.Errorf("%s.devices[%d]: not a clean path under %s", field, j, adoptDevicePrefix)
			}
			if seenDevices[device] {
				return fmt.Errorf("%s.devices[%d]: duplicate %q", field, j, device)
			}
			seenDevices[device] = true
		}
	}
	return nil
}

func validateAdoptMonitors(adopt v1alpha1.AdoptSpec) error {
	if !slices.Contains([]int{1, 3, 5}, len(adopt.Monitors)) {
		return fmt.Errorf("storage.adopt.monitors: need 1, 3 or 5 monitors, got %d", len(adopt.Monitors))
	}
	for i, monitor := range adopt.Monitors {
		field := fmt.Sprintf("storage.adopt.monitors[%d]", i)
		if err := validateAdoptMonitor(field, monitor, adopt.Hosts); err != nil {
			return err
		}
		for _, earlier := range adopt.Monitors[:i] {
			switch {
			case earlier.ID == monitor.ID:
				return fmt.Errorf("%s.id: duplicate %q", field, monitor.ID)
			case earlier.Address == monitor.Address:
				return fmt.Errorf("%s.address: duplicate %q", field, monitor.Address)
			case earlier.Node == monitor.Node:
				return fmt.Errorf("%s.node: duplicate %q", field, monitor.Node)
			}
		}
	}
	return nil
}

func validateAdoptMonitor(field string, monitor v1alpha1.AdoptMonitor, hosts []v1alpha1.AdoptHost) error {
	if !adoptMonitorID.MatchString(monitor.ID) {
		return fmt.Errorf("%s.id: not made of lowercase letters", field)
	}
	addr, err := netip.ParseAddr(monitor.Address)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("%s.address: not an IPv4 address", field)
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() {
		return fmt.Errorf("%s.address: unspecified, loopback or multicast address", field)
	}
	if len(validation.IsDNS1123Label(monitor.Node)) > 0 {
		return fmt.Errorf("%s.node: not a DNS-1123 label", field)
	}
	if !slices.ContainsFunc(hosts, func(host v1alpha1.AdoptHost) bool { return host.Node == monitor.Node }) {
		return fmt.Errorf("%s.node: %q is not in storage.adopt.hosts", field, monitor.Node)
	}
	return nil
}

func validatePlatformHost(host string) error {
	if host == "" {
		return fmt.Errorf("spec.platform.host is required: set the domain of the platform, for example cloud.example.com")
	}
	if !dnsname.Valid(host) {
		return fmt.Errorf("spec.platform.host %q is not a valid DNS name", host)
	}
	return nil
}

func validateKMSEndpoint(kms *v1alpha1.KMSSpec) error {
	if kms == nil {
		return nil
	}
	path, ok := strings.CutPrefix(kms.Endpoint, "unix://")
	if !ok || path == "/" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return fmt.Errorf("spec.encryption.kms.endpoint must be unix:///<absolute socket path>, got %q", kms.Endpoint)
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
			Encryption:      *cfg.Spec.Encryption.DeepCopy(),
			JoinCIDR:        cfg.Spec.Network.Fabric.JoinCIDR,
			PodMTU:          cfg.Spec.Network.PodMTU,
			ManagementCIDR:  cfg.Spec.Network.ManagementCIDR,
			TenantLegs:      cfg.DeepCopy().Spec.Network.TenantLegs,
			Storage:         v1alpha1.ClusterStorageSpec{Adopt: adoptWithoutKeyFile(cfg.Spec.Storage.Adopt)},
		},
	}
}

func adoptWithoutKeyFile(adopt *v1alpha1.AdoptSpec) *v1alpha1.AdoptSpec {
	if adopt == nil {
		return nil
	}
	out := adopt.DeepCopy()
	out.KeyFile = ""
	return out
}

func ToHost(cfg v1alpha1.ClusterConfig, nodeName string) v1alpha1.Host {
	return v1alpha1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{v1alpha1.LabelKind: "Host", v1alpha1.LabelName: nodeName}},
		Spec: v1alpha1.HostSpec{
			Roles:             slices.Clone(cfg.Spec.Roles),
			Management:        v1alpha1.ManagementSpec{Enabled: cfg.Spec.Host.Management},
			MaintenanceWindow: cfg.Spec.Host.MaintenanceWindow,
			Storage:           v1alpha1.HostStorageSpec{Devices: slices.Concat(cfg.Spec.Storage.Devices, v1alpha1.AdoptHostDevices(cfg.Spec.Storage.Adopt, nodeName))},
		},
	}
}

func addonSettingValues(addons map[string]bool) map[string]string {
	values := map[string]string{}
	for _, def := range settings.Catalog() {
		name, isAddon := strings.CutSuffix(def.Key, ".enabled")
		if enabled, set := addons[name]; isAddon && set {
			values[def.Key] = strconv.FormatBool(enabled)
		}
	}
	return values
}

func SettingValues(cfg v1alpha1.ClusterConfig) map[string]string {
	values := map[string]string{
		"platform.host":                 cfg.Spec.Platform.Host,
		"platform.tls-mode":             cfg.Spec.Platform.TLSMode,
		"storage.replicas":              strconv.Itoa(int(cfg.Spec.Storage.Replicas)),
		"storage.cephx-cipher":          fallback(cfg.Spec.Storage.CephxCipher, cephx.Aes),
		"storage.network":               vlanValue(cfg.Spec.Network.StorageVlan),
		"migration.network":             vlanValue(cfg.Spec.Network.MigrationVlan),
		"virt.emulation":                strconv.FormatBool(cfg.Spec.Virtualization.Emulation),
		"kata.enabled":                  strconv.FormatBool(cfg.Spec.Addons["kata"]),
		"loki.enabled":                  strconv.FormatBool(cfg.Spec.Addons["loki"]),
		"clusters.enabled":              strconv.FormatBool(cfg.Spec.Addons["clusters"]),
		"descheduler.enabled":           strconv.FormatBool(cfg.Spec.Addons["descheduler"]),
		"network.eip-mode":              cfg.Spec.Network.Fabric.EIPMode,
		"network.provider-networks":     providerNetworksValue(cfg),
		"dns.upstreams":                 strings.Join(cfg.Spec.DNS.Upstreams, " "),
		"egress.http-proxy":             cfg.Spec.Egress.HTTPProxy,
		"egress.no-proxy":               strings.Join(cfg.Spec.Egress.NoProxy, ","),
		"letsencrypt.dns01-nameservers": strings.Join(cfg.Spec.Egress.DNS01Nameservers, ","),
	}
	maps.Copy(values, addonSettingValues(cfg.Spec.Addons))
	return values
}

func ToSettings(cfg v1alpha1.ClusterConfig) []v1alpha1.Setting {
	values := SettingValues(cfg)
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

func providerNetworksValue(cfg v1alpha1.ClusterConfig) string {
	value, _ := settings.EncodeProviderNetworks(cfg.Spec.Network.Fabric.ProviderNetworks)
	return value
}

func vlanValue(vlan int32) string {
	if vlan == 0 {
		return ""
	}
	return strconv.Itoa(int(vlan))
}
