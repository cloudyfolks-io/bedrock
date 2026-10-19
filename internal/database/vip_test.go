package database

import (
	"context"
	"net/netip"
	"sort"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func addrs(values ...string) map[netip.Addr]bool {
	taken := map[netip.Addr]bool{}
	for _, value := range values {
		taken[netip.MustParseAddr(value)] = true
	}
	return taken
}

func TestLowestFreeVIP(t *testing.T) {
	prefix := netip.MustParsePrefix("100.66.16.0/21")
	cases := map[string]struct {
		taken map[netip.Addr]bool
		want  string
	}{
		"first host":      {addrs(), "100.66.16.1"},
		"a gap":           {addrs("100.66.16.1", "100.66.16.3"), "100.66.16.2"},
		"outside ignored": {addrs("10.0.0.1"), "100.66.16.1"},
	}
	for name, tc := range cases {
		if got, err := LowestFreeVIP(prefix, tc.taken); err != nil || got != tc.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	small := netip.MustParsePrefix("100.66.16.0/30")
	if got, err := LowestFreeVIP(small, addrs("100.66.16.1")); err != nil || got != "100.66.16.2" {
		t.Fatalf("small range: %q %v", got, err)
	}
	if _, err := LowestFreeVIP(small, addrs("100.66.16.1", "100.66.16.2")); err != ErrVIPRangeFull {
		t.Fatalf("full range: %v", err)
	}
	if _, err := LowestFreeVIP(netip.Prefix{}, nil); err != ErrVIPRangeMissing {
		t.Fatalf("no range: %v", err)
	}
}

func TestServiceAddressesCoverEveryWayToHoldAnAddress(t *testing.T) {
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationKubeVIP: "100.66.16.1, 100.66.16.2"}},
		Spec:       corev1.ServiceSpec{LoadBalancerIP: "100.66.16.3", ExternalIPs: []string{"100.66.16.4"}},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "100.66.16.5"}}}},
	}
	got := ServiceAddresses(service)
	sort.Strings(got)
	want := []string{"100.66.16.1", "100.66.16.2", "100.66.16.3", "100.66.16.4", "100.66.16.5"}
	for _, address := range want {
		found := false
		for _, held := range got {
			found = found || held == address
		}
		if !found {
			t.Errorf("%s missing from %v", address, got)
		}
	}
}

func TestVIPServiceGolden(t *testing.T) {
	service, err := VIPService(goldenDatabase(), goldenProject(), "100.66.16.2", 5432)
	if err != nil {
		t.Fatal(err)
	}
	goldenList(t, "vip_service", []*unstructured.Unstructured{service})
	if _, err := VIPService(goldenDatabase(), goldenProject(), "nonsense", 5432); err == nil {
		t.Fatal("a bad address was accepted")
	}
}

func vipConfig(t *testing.T, cidr string) Config {
	t.Helper()
	cfg, err := ParseConfig(map[string]string{"database.vip-range": cidr})
	must(t, err)
	return cfg
}

func TestEnsureVIPAllocatesTheLowestFreeAddressAndKeepsItAcrossARestart(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	ensurePlatformNamespace(t, c)
	cfg := vipConfig(t, "100.77.0.0/24")
	first, project := storedDatabase(t, c, nil)
	second, _ := storedDatabase(t, c, nil)

	a := &Allocator{}
	vip1, err := a.EnsureVIP(ctx, c, c, cfg, *first, project, 5432)
	must(t, err)
	vip2, err := a.EnsureVIP(ctx, c, c, cfg, *second, project, 5432)
	must(t, err)
	if vip1 == vip2 || vip1 != "100.77.0.1" || vip2 != "100.77.0.2" {
		t.Fatalf("addresses %s %s", vip1, vip2)
	}
	restarted := &Allocator{}
	again, err := restarted.EnsureVIP(ctx, c, c, cfg, *first, project, 5432)
	must(t, err)
	if again != vip1 {
		t.Fatalf("a restart moved the address from %s to %s", vip1, again)
	}
	var service corev1.Service
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(first.UID)}, &service))
}

func TestEnsureVIPGivesTwoConcurrentCreatesTwoAddresses(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	ensurePlatformNamespace(t, c)
	cfg := vipConfig(t, "100.78.0.0/24")
	project := testProject("x")
	a := &Allocator{}
	var databases [6]*string
	results := make([]string, len(databases))
	var wg sync.WaitGroup
	for i := range databases {
		db, _ := storedDatabase(t, c, nil)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vip, err := a.EnsureVIP(ctx, c, c, cfg, *db, project, 5432)
			if err != nil {
				t.Error(err)
			}
			results[i] = vip
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, vip := range results {
		if vip == "" || seen[vip] {
			t.Fatalf("addresses %v", results)
		}
		seen[vip] = true
	}
}

func TestEnsureVIPSkipsAddressesThatOtherServicesHold(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	ensurePlatformNamespace(t, c)
	ensureNamespace(t, c, "traefik")
	cfg := vipConfig(t, "100.79.0.0/24")
	create(t, c, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "edge-" + nextSequence(), Namespace: "traefik", Annotations: map[string]string{annotationKubeVIP: "100.79.0.1"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
	})
	db, project := storedDatabase(t, c, nil)
	vip, err := (&Allocator{}).EnsureVIP(ctx, c, c, cfg, *db, project, 5432)
	must(t, err)
	if vip != "100.79.0.2" {
		t.Fatalf("vip %s", vip)
	}
}

func TestEnsureVIPWithoutARangeIsRefusedAndMakesNoService(t *testing.T) {
	c := startEnv(t)
	ensurePlatformNamespace(t, c)
	db, project := storedDatabase(t, c, nil)
	_, err := (&Allocator{}).EnsureVIP(context.Background(), c, c, vipConfig(t, ""), *db, project, 5432)
	if err != ErrVIPRangeMissing {
		t.Fatalf("err %v", err)
	}
	var service corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(db.UID)}, &service); err == nil {
		t.Fatal("a service was made without a range")
	}
}
