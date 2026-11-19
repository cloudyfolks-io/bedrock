package database

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	annotationKubeVIP          = "kube-vip.io/loadbalancerIPs"
	annotationKubeVIPInterface = "kube-vip.io/serviceInterface"
)

var (
	ErrVIPRangeMissing = errors.New("the Setting database.vip-range is empty")
	ErrVIPRangeFull    = errors.New("the VIP range has no free address")
	ErrVIPNameTaken    = errors.New("a Service of the gate address name exists and the database does not own it")
)

func LowestFreeVIP(prefix netip.Prefix, taken map[netip.Addr]bool) (string, error) {
	if !prefix.IsValid() {
		return "", ErrVIPRangeMissing
	}
	first, last := prefix.Masked().Addr().Next(), lastOf(prefix)
	for addr := first; addr.IsValid() && addr.Less(last) && prefix.Contains(addr); addr = addr.Next() {
		if !taken[addr] {
			return addr.String(), nil
		}
	}
	return "", ErrVIPRangeFull
}

func lastOf(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Masked().Addr().AsSlice()
	for bit := prefix.Bits(); bit < len(bytes)*8; bit++ {
		bytes[bit/8] |= 1 << (7 - bit%8)
	}
	addr, _ := netip.AddrFromSlice(bytes)
	return addr
}

func ServiceAddresses(service corev1.Service) []string {
	var found []string
	for _, address := range strings.Split(service.Annotations[annotationKubeVIP], ",") {
		found = append(found, strings.TrimSpace(address))
	}
	found = append(found, service.Spec.LoadBalancerIP)
	found = append(found, service.Spec.ExternalIPs...)
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		found = append(found, ingress.IP)
	}
	return found
}

func TakenVIPs(ctx context.Context, live client.Reader) (map[netip.Addr]bool, error) {
	var services corev1.ServiceList
	if err := live.List(ctx, &services); err != nil {
		return nil, err
	}
	taken := map[netip.Addr]bool{}
	for _, service := range services.Items {
		for _, address := range ServiceAddresses(service) {
			if addr, err := netip.ParseAddr(address); err == nil {
				taken[addr] = true
			}
		}
	}
	return taken, nil
}

func VIPService(db v1alpha1.Database, project Project, vip, iface string, port int) (*unstructured.Unstructured, error) {
	if _, err := netip.ParseAddr(vip); err != nil || !validPort(port) {
		return nil, fmt.Errorf("the gate address needs a valid IP and port")
	}
	annotations := map[string]string{annotationKubeVIP: vip}
	if iface != "" {
		annotations[annotationKubeVIPInterface] = iface
	}
	service, err := toUnstructured(&corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name: VIPName(db.UID), Namespace: PlatformNamespace,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeLoadBalancer,
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			Selector:              gateSelector(db.UID),
			Ports:                 []corev1.ServicePort{{Name: "db", Port: int32(port), TargetPort: intstr.FromInt32(int32(port)), Protocol: corev1.ProtocolTCP}},
		},
	})
	if err != nil {
		return nil, err
	}
	return Adopt(service, db, project), nil
}

func managedGateService(db v1alpha1.Database, project Project, port int) (*unstructured.Unstructured, error) {
	listener := int32(port)
	service, err := toUnstructured(&corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: VIPName(db.UID), Namespace: PlatformNamespace},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP, Selector: gateSelector(db.UID),
			Ports: []corev1.ServicePort{{Name: "db", Port: listener, TargetPort: intstr.FromInt32(listener), Protocol: corev1.ProtocolTCP}},
		},
	})
	if err != nil {
		return nil, err
	}
	return Adopt(service, db, project), nil
}

func (r *Reconciler) gateAddress(ctx context.Context, in *flow) (string, error) {
	db := *in.db
	if in.legacy == nil {
		return r.Alloc.EnsureVIP(ctx, r.Client, r.Live, in.cfg, db, in.project, in.port)
	}
	want, err := managedGateService(db, in.project, in.port)
	if err != nil {
		return "", err
	}
	var service unstructured.Unstructured
	service.SetGroupVersionKind(ServiceGVK)
	key := client.ObjectKeyFromObject(want)
	err = r.Live.Get(ctx, key, &service)
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}
	if err == nil && !ownedFrontend(&service, db) {
		return "", ErrVIPNameTaken
	}
	if err := ssa.Apply(ctx, r.Client, want, v1alpha1.DatabaseFieldManager); err != nil {
		return "", err
	}
	if err := r.Live.Get(ctx, key, &service); err != nil {
		return "", err
	}
	address, _, _ := unstructured.NestedString(service.Object, "spec", "clusterIP")
	if !parseAddr(address).Is4() {
		return "", fmt.Errorf("the managed database gate Service needs an IPv4 cluster address")
	}
	return address, nil
}

type Allocator struct {
	mu sync.Mutex
}

func (a *Allocator) EnsureVIP(ctx context.Context, c client.Client, live client.Reader, cfg Config, db v1alpha1.Database, project Project, port int) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var existing corev1.Service
	err := live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(db.UID)}, &existing)
	found := err == nil
	if found {
		if existing.Labels[LabelInstance] != Name(db.UID) {
			return "", ErrVIPNameTaken
		}
		if address := strings.TrimSpace(existing.Annotations[annotationKubeVIP]); address != "" {
			if existing.Annotations[annotationKubeVIPInterface] == cfg.VIPInterface {
				return address, nil
			}
			service, err := VIPService(db, project, address, cfg.VIPInterface, port)
			if err != nil {
				return "", err
			}
			if err := c.Delete(ctx, &existing); client.IgnoreNotFound(err) != nil {
				return "", err
			}
			return address, c.Create(ctx, service, client.FieldOwner(v1alpha1.DatabaseFieldManager))
		}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if !cfg.VIPRange.IsValid() {
		return "", ErrVIPRangeMissing
	}
	taken, err := TakenVIPs(ctx, live)
	if err != nil {
		return "", err
	}
	vip, err := LowestFreeVIP(cfg.VIPRange, taken)
	if err != nil {
		return "", err
	}
	service, err := VIPService(db, project, vip, cfg.VIPInterface, port)
	if err != nil {
		return "", err
	}
	if found {
		before := existing.DeepCopy()
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		for key, value := range service.GetAnnotations() {
			existing.Annotations[key] = value
		}
		return vip, c.Patch(ctx, &existing, client.MergeFrom(before), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	}
	if err := c.Create(ctx, service, client.FieldOwner(v1alpha1.DatabaseFieldManager)); err != nil {
		return "", err
	}
	return vip, nil
}
