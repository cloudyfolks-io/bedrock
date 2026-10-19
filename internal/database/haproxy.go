package database

import (
	"fmt"
	"maps"
	"net"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	CertDir           = "/certs"
	ConfigFile        = "haproxy.cfg"
	CombinedPEMFile   = "server.pem"
	TenantPEMFile     = "tenant.pem"
	CAFile            = "ca.crt"
	HealthPort        = int32(8404)
	ServerNameSuffix  = ".dbaas.internal"
	proxyUserID       = int64(1000)
	ComponentProxy    = "endpoint"
	HAProxyRepository = "docker.io/library/haproxy"
)

var (
	uidPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	serverNamePattern = regexp.MustCompile(`^[a-z0-9.-]+$`)
)

type GateSpec struct {
	ListenPort  int
	BackendHost string
	BackendPort int
	ClientCN    string
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, r := range host {
		if r != '-' && r != '.' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

func ServerName(uid string) string { return uid + ServerNameSuffix }

func GateConfig(s GateSpec) (string, error) {
	if !uidPattern.MatchString(s.ClientCN) {
		return "", fmt.Errorf("the client certificate name must be a database UID")
	}
	if !validHost(s.BackendHost) || !validPort(s.BackendPort) || !validPort(s.ListenPort) {
		return "", fmt.Errorf("invalid backend host or port")
	}
	return strings.Join([]string{
		"global",
		"    maxconn 4096",
		"defaults",
		"    mode tcp",
		"    timeout connect 5s",
		"    timeout client 1h",
		"    timeout server 1h",
		"frontend db",
		fmt.Sprintf("    bind :%d ssl crt %s/%s ssl-min-ver TLSv1.2 ca-file %s/%s verify required", s.ListenPort, CertDir, CombinedPEMFile, CertDir, CAFile),
		fmt.Sprintf("    tcp-request session reject unless { ssl_c_s_dn(CN) -m str %s }", s.ClientCN),
		"    default_backend engine",
		"backend engine",
		fmt.Sprintf("    server db %s:%d check", s.BackendHost, s.BackendPort),
		"frontend health",
		"    mode http",
		fmt.Sprintf("    bind :%d", HealthPort),
		"    monitor-uri /healthz",
		"    monitor fail if { nbsrv(engine) lt 1 }",
		"",
	}, "\n"), nil
}

func ProxyConfig(vip string, port int, serverName string) (string, error) {
	if net.ParseIP(vip) == nil || !validPort(port) || !serverNamePattern.MatchString(serverName) {
		return "", fmt.Errorf("invalid gate address, port or server name")
	}
	return strings.Join([]string{
		"global",
		"    maxconn 2048",
		"defaults",
		"    mode tcp",
		"    timeout connect 5s",
		"    timeout client 1h",
		"    timeout server 1h",
		"frontend db",
		fmt.Sprintf("    bind :%d", port),
		"    default_backend gate",
		"backend gate",
		fmt.Sprintf("    server gate %s:%d ssl crt %s/%s ssl-min-ver TLSv1.2 ca-file %s/%s verify required sni str(%s) verifyhost %s check check-ssl",
			vip, port, CertDir, TenantPEMFile, CertDir, CAFile, serverName, serverName),
		"frontend health",
		"    mode http",
		fmt.Sprintf("    bind :%d", HealthPort),
		"    monitor-uri /healthz",
		"    monitor fail if { nbsrv(gate) lt 1 }",
		"",
	}, "\n"), nil
}

func EndpointImage(images []string) (string, bool) {
	for _, image := range images {
		if strings.HasPrefix(image, HAProxyRepository+":") || strings.HasPrefix(image, HAProxyRepository+"@") {
			return image, true
		}
	}
	return "", false
}

type proxyDeployment struct {
	Name           string
	Namespace      string
	Labels         map[string]string
	PodLabels      map[string]string
	Selector       map[string]string
	PodAnnotations map[string]string
	OwnerRefs      []metav1.OwnerReference
	Image          string
	Port           int
	Volume         corev1.VolumeSource
	RuntimeClass   string
	DNSServers     []string
}

func deploymentOf(p proxyDeployment) (*unstructured.Unstructured, error) {
	if p.Name == "" || p.Namespace == "" || p.Image == "" || !validPort(p.Port) {
		return nil, fmt.Errorf("a proxy deployment needs a name, a namespace, an image and a port")
	}
	probe := func(initial int32) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(HealthPort)}},
			InitialDelaySeconds: initial, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 3,
		}
	}
	resources := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}
	pod := corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		SecurityContext:              &corev1.PodSecurityContext{FSGroup: ptr.To(proxyUserID), RunAsUser: ptr.To(proxyUserID), RunAsGroup: ptr.To(proxyUserID), RunAsNonRoot: ptr.To(true)},
		Containers: []corev1.Container{{
			Name:            "haproxy",
			Image:           p.Image,
			Command:         []string{"haproxy", "-f", CertDir + "/" + ConfigFile},
			Ports:           []corev1.ContainerPort{{Name: "db", ContainerPort: int32(p.Port)}},
			Resources:       corev1.ResourceRequirements{Requests: resources, Limits: resources},
			VolumeMounts:    []corev1.VolumeMount{{Name: "certs", MountPath: CertDir, ReadOnly: true}},
			ReadinessProbe:  probe(2),
			LivenessProbe:   probe(15),
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
		}},
		Volumes: []corev1.Volume{{Name: "certs", VolumeSource: p.Volume}},
	}
	if p.RuntimeClass != "" {
		pod.RuntimeClassName = ptr.To(p.RuntimeClass)
	}
	if len(p.DNSServers) > 0 {
		pod.DNSPolicy = corev1.DNSNone
		pod.DNSConfig = &corev1.PodDNSConfig{Nameservers: append([]string(nil), p.DNSServers...)}
	}
	deployment := appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace, Labels: maps.Clone(p.Labels), OwnerReferences: p.OwnerRefs},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: p.Selector},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(p.PodLabels), Annotations: maps.Clone(p.PodAnnotations)},
				Spec:       pod,
			},
		},
	}
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
	if err != nil {
		return nil, fmt.Errorf("convert the deployment: %w", err)
	}
	obj := &unstructured.Unstructured{Object: body}
	unstructured.RemoveNestedField(obj.Object, "status")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(obj.Object, "spec", "template", "metadata", "creationTimestamp")
	return obj, nil
}

func projectedCerts(secretName, secretKey, secretFile, configMapName string) corev1.VolumeSource {
	mode := int32(0o440)
	return corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		DefaultMode: &mode,
		Sources: []corev1.VolumeProjection{
			{Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Items:                []corev1.KeyToPath{{Key: secretKey, Path: secretFile}, {Key: "ca.crt", Path: CAFile}},
			}},
			{ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
				Items:                []corev1.KeyToPath{{Key: ConfigFile, Path: ConfigFile}},
			}},
		},
	}}
}
