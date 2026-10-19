package database

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const SuffixEndpoint = "endpoint"

func EndpointName(db v1alpha1.Database) string { return db.Name + "-" + SuffixEndpoint }

func SecurityGroupName(uid types.UID) string {
	name := string(uid)
	if len(name) > 8 {
		name = name[:8]
	}
	return "database-" + name
}

func ownerRefOf(db v1alpha1.Database) metav1.OwnerReference {
	return *metav1.NewControllerRef(&db, v1alpha1.GroupVersion.WithKind(Kind))
}

func projectLabels(db v1alpha1.Database, project Project) map[string]string {
	labels := v1alpha1.TenantLabels(Kind, db.Name, project.Tenant, project.Name)
	labels[LabelInstance] = Name(db.UID)
	labels[LabelManagedBy] = ManagedBy
	return labels
}

type ProxyInput struct {
	Database     v1alpha1.Database
	Project      Project
	Image        string
	Subnet       v1alpha1.Subnet
	Address      string
	VIP          string
	Port         int
	RuntimeClass string
	DNSServers   []string
	Revision     int64
}

func proxySelector(db v1alpha1.Database) map[string]string {
	return map[string]string{LabelInstance: Name(db.UID), LabelComponent: ComponentProxy}
}

func ProxyObjects(in ProxyInput) ([]*unstructured.Unstructured, error) {
	db := in.Database
	config, err := ProxyConfig(in.VIP, in.Port, ServerName(Name(db.UID)))
	if err != nil {
		return nil, err
	}
	if len(in.DNSServers) == 0 {
		return nil, fmt.Errorf("the endpoint needs DNS servers")
	}
	labels, owners := projectLabels(db, in.Project), []metav1.OwnerReference{ownerRefOf(db)}
	configMap, err := toUnstructured(&corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: EndpointName(db), Namespace: db.Namespace, Labels: labels, OwnerReferences: owners},
		Data:       map[string]string{ConfigFile: config},
	})
	if err != nil {
		return nil, err
	}
	certificate := CertificateObject(CertSpec{
		Name: EndpointName(db), Namespace: db.Namespace, SecretName: EndpointName(db), CommonName: Name(db.UID),
		Usages: ClientCertUsages(), Labels: labels, OwnerRefs: owners,
	})
	attachment := network.PodAttachment(network.NIC{Namespace: db.Namespace, Subnet: in.Subnet.Name, Address: in.Address, SecurityGroups: []string{SecurityGroupName(db.UID)}})
	podLabels := map[string]string{LabelComponent: ComponentProxy}
	for key, value := range labels {
		podLabels[key] = value
	}
	for key, value := range attachment.Labels {
		podLabels[key] = value
	}
	annotations := map[string]string{annotationConfigHash: configHash(config, strconv.FormatInt(in.Revision, 10))}
	for key, value := range attachment.Annotations {
		annotations[key] = value
	}
	deployment, err := deploymentOf(proxyDeployment{
		Name: EndpointName(db), Namespace: db.Namespace, Labels: labels, PodLabels: podLabels, Selector: proxySelector(db),
		PodAnnotations: annotations, OwnerRefs: owners, Image: in.Image, Port: in.Port,
		Volume:       projectedCerts(EndpointName(db), CombinedPEMKey, TenantPEMFile, EndpointName(db)),
		RuntimeClass: in.RuntimeClass, DNSServers: in.DNSServers,
	})
	if err != nil {
		return nil, err
	}
	return []*unstructured.Unstructured{configMap, certificate, deployment}, nil
}

func AllowedCIDRs(db v1alpha1.Database, subnet v1alpha1.Subnet) []string {
	if len(db.Spec.AllowedCIDRs) > 0 {
		return slices.Clone(db.Spec.AllowedCIDRs)
	}
	return []string{subnet.Spec.CIDR}
}

func SecurityGroupObject(db v1alpha1.Database, project Project, subnet v1alpha1.Subnet, vip string, port int) *v1alpha1.SecurityGroup {
	cidrs := AllowedCIDRs(db, subnet)
	ingress := make([]v1alpha1.SecurityGroupRule, 0, len(cidrs)+1)
	for _, cidr := range cidrs {
		ingress = append(ingress, v1alpha1.SecurityGroupRule{Protocol: "tcp", Port: int32(port), CIDR: cidr})
	}
	if vip != "" {
		ingress = append(ingress, v1alpha1.SecurityGroupRule{Protocol: "tcp", Port: int32(port), CIDR: vip + "/32"})
	}
	return &v1alpha1.SecurityGroup{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SecurityGroup"},
		ObjectMeta: metav1.ObjectMeta{
			Name: SecurityGroupName(db.UID), Namespace: db.Namespace,
			Labels: projectLabels(db, project), OwnerReferences: []metav1.OwnerReference{ownerRefOf(db)},
		},
		Spec: v1alpha1.SecurityGroupSpec{
			Ingress: ingress,
			Egress:  []v1alpha1.SecurityGroupRule{{Protocol: "all", CIDR: "0.0.0.0/0"}},
		},
	}
}

func AddressOf(db v1alpha1.Database, subnet string) (string, bool) {
	for _, address := range db.Status.Addresses {
		if address.Subnet == subnet && address.IP != "" {
			return address.IP, true
		}
	}
	return "", false
}

func pinnedByProxy(deployment *unstructured.Unstructured) string {
	annotations, _, _ := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "annotations")
	for key, value := range annotations {
		if network.IsAddressAnnotation(key) {
			return value
		}
	}
	return ""
}

func (a *Allocator) EnsureAddress(ctx context.Context, c client.Client, live client.Reader, db *v1alpha1.Database, subnet v1alpha1.Subnet, port int32) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if address, found := AddressOf(*db, subnet.Name); found {
		return address, nil
	}
	if address := a.proxyAddress(ctx, live, *db); address != "" {
		return address, a.record(ctx, c, db, subnet.Name, address, port)
	}
	reserved, err := a.reservedByDatabases(ctx, live, *db, subnet.Name)
	if err != nil {
		return "", err
	}
	address, err := network.AllocateAddress(ctx, live, subnet, reserved)
	if err != nil {
		return "", err
	}
	return address, a.record(ctx, c, db, subnet.Name, address, port)
}

func (a *Allocator) proxyAddress(ctx context.Context, live client.Reader, db v1alpha1.Database) string {
	deployment := &unstructured.Unstructured{}
	deployment.SetGroupVersionKind(DeploymentGVK)
	if err := live.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: EndpointName(db)}, deployment); err != nil {
		return ""
	}
	if address := pinnedByProxy(deployment); parseAddr(address).IsValid() {
		return address
	}
	return ""
}

func parseAddr(address string) netip.Addr {
	addr, _ := netip.ParseAddr(address)
	return addr
}

func (a *Allocator) reservedByDatabases(ctx context.Context, live client.Reader, db v1alpha1.Database, subnet string) ([]string, error) {
	var databases v1alpha1.DatabaseList
	if err := live.List(ctx, &databases, client.InNamespace(db.Namespace)); err != nil {
		return nil, err
	}
	var reserved []string
	for _, other := range databases.Items {
		if other.UID == db.UID {
			continue
		}
		if address, found := AddressOf(other, subnet); found {
			reserved = append(reserved, address)
		}
	}
	return reserved, nil
}

func (a *Allocator) record(ctx context.Context, c client.Client, db *v1alpha1.Database, subnet, address string, port int32) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Database
		if err := c.Get(ctx, client.ObjectKeyFromObject(db), &current); err != nil {
			return err
		}
		if existing, found := AddressOf(current, subnet); found && existing != address {
			return errors.New("another address was recorded first")
		}
		current.Status.Addresses = []v1alpha1.DatabaseAddress{{Subnet: subnet, IP: address, Port: port}}
		if err := c.Status().Update(ctx, &current, client.FieldOwner(v1alpha1.DatabaseFieldManager)); err != nil {
			return err
		}
		*db = current
		return nil
	})
}
