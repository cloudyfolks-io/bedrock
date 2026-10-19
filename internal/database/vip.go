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
)

const annotationKubeVIP = "kube-vip.io/loadbalancerIPs"

var (
	ErrVIPRangeMissing = errors.New("the Setting database.vip-range is empty")
	ErrVIPRangeFull    = errors.New("the VIP range has no free address")
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

func VIPService(db v1alpha1.Database, project Project, vip string, port int) (*unstructured.Unstructured, error) {
	if _, err := netip.ParseAddr(vip); err != nil || !validPort(port) {
		return nil, fmt.Errorf("the gate address needs a valid IP and port")
	}
	service, err := toUnstructured(&corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name: VIPName(db.UID), Namespace: PlatformNamespace,
			Annotations: map[string]string{annotationKubeVIP: vip},
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

type Allocator struct {
	mu sync.Mutex
}

func (a *Allocator) EnsureVIP(ctx context.Context, c client.Client, live client.Reader, cfg Config, db v1alpha1.Database, project Project, port int) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var existing corev1.Service
	err := live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(db.UID)}, &existing)
	if err == nil {
		if address := strings.TrimSpace(existing.Annotations[annotationKubeVIP]); address != "" {
			return address, nil
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
	service, err := VIPService(db, project, vip, port)
	if err != nil {
		return "", err
	}
	if err := c.Create(ctx, service, client.FieldOwner(v1alpha1.DatabaseFieldManager)); err != nil {
		return "", err
	}
	return vip, nil
}
