package network

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

var (
	chainOnce   sync.Once
	chainServer *envtest.Environment
	chainClient client.Client
	chainConfig *rest.Config
	chainErr    error
)

func chainEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	chainOnce.Do(func() { chainServer, chainClient, chainConfig, chainErr = startEnv(testenv.CRDPaths()) })
	if chainErr != nil {
		t.Fatal(chainErr)
	}
	return chainClient, chainConfig
}

func stopChainEnv() {
	if chainServer != nil {
		_ = chainServer.Stop()
	}
}

func newManager(t *testing.T, cfg *rest.Config) ctrl.Manager {
	t.Helper()
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestSetupSkipsWithoutFabricKinds(t *testing.T) {
	server, _, cfg, err := startEnv(testenv.CRDPaths()[:1])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	mgr := newManager(t, cfg)
	if err := SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("a manager without registered controllers must start: %v", err)
	}
}

func startChain(t *testing.T) (client.Client, context.Context) {
	t.Helper()
	c, cfg := chainEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := newManager(t, cfg)
	if err := SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()
	go fabrictest.Auto(ctx, c, 200*time.Millisecond)
	return c, ctx
}

func putSetting(t *testing.T, c client.Client, key, value string) {
	t.Helper()
	requireAccepted(t, c, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}, Spec: v1alpha1.SettingSpec{Value: value}})
}

func eventually(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 250*time.Millisecond, timeout, true, func(context.Context) (bool, error) {
		return condition(), nil
	})
	if err != nil {
		t.Fatalf("not within %s: %s", timeout, what)
	}
}

func isReady(c client.Client, obj client.Object) bool {
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
		return false
	}
	switch o := obj.(type) {
	case *v1alpha1.Network:
		return v1alpha1.IsConditionTrue(o.Status.Conditions, v1alpha1.ConditionReady) && v1alpha1.IsConditionTrue(o.Status.Conditions, ConditionExternalReady)
	case *v1alpha1.Subnet:
		return v1alpha1.IsConditionTrue(o.Status.Conditions, v1alpha1.ConditionReady)
	case *v1alpha1.SecurityGroup:
		return v1alpha1.IsConditionTrue(o.Status.Conditions, v1alpha1.ConditionReady)
	case *v1alpha1.ElasticIP:
		return v1alpha1.IsConditionTrue(o.Status.Conditions, v1alpha1.ConditionReady)
	case *v1alpha1.LoadBalancer:
		return v1alpha1.IsConditionTrue(o.Status.Conditions, v1alpha1.ConditionReady)
	case *v1alpha1.DNSZone:
		return v1alpha1.IsConditionTrue(o.Status.Conditions, v1alpha1.ConditionReady)
	}
	return false
}

func TestManagerProgramsTheWholeChain(t *testing.T) {
	c, ctx := startChain(t)
	putSetting(t, c, "network.provider-networks", `[{"name":"public","interface":"bond0","cidr":"192.0.2.0/24","gateway":"192.0.2.1"}]`)
	ns := newNamespaceIn(t, c, "chain", "net")
	network := networkOf("chain", "chain", "10.100.0.0/16")
	network.Spec.EnableExternal = true
	requireAccepted(t, c, network)
	def := policySubnet(ns, "default", "chain", "10.100.1.0/24")
	other := policySubnet(ns, "other", "chain", "10.100.2.0/24")
	other.Spec.DNSZoneRef = "zone1"
	web := securityGroupOf(ns, "web", []v1alpha1.SecurityGroupRule{tcpRule(80, "0.0.0.0/0")}, nil)
	vm := virtualMachineUsing(ns, "vm1", "web")
	eip1 := elasticIPOf(ns, "eip1", "")
	eip1.Spec.Target = vmTarget("vm1")
	eip2 := elasticIPOf(ns, "eip2", "")
	eip2.Spec.Target = lbTarget("lb1")
	lb := loadBalancerFor(ns, "lb1", "eip2")
	lb.Spec.Listeners[0].Targets = []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 8080}}
	zone := zoneFor(ns, "zone1", dbRecord)
	for _, obj := range []client.Object{def, other, web, vm} {
		requireAccepted(t, c, obj)
	}
	if err := fabrictest.CreateIP(ctx, c, "vm1."+ns+".default", ns, "vm1", FabricName(ns, "default"), "10.100.1.5"); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{eip1, eip2, lb, zone} {
		requireAccepted(t, c, obj)
	}
	ready := []client.Object{network, def, other, web, eip1, eip2, lb, zone}
	eventually(t, 90*time.Second, "every object is ready", func() bool {
		for _, obj := range ready {
			if !isReady(c, obj) {
				return false
			}
		}
		return true
	})
	if prefix := netip.MustParsePrefix("192.0.2.0/24"); !prefix.Contains(netip.MustParseAddr(eip1.Status.Address)) {
		t.Fatalf("address %q", eip1.Status.Address)
	}
	for _, obj := range []client.Object{zone, lb, eip2, eip1, vm, web, def, other, network} {
		if err := c.Delete(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 90*time.Second, "every Fabric object of the chain is gone", func() bool {
		return len(leftovers(t, c, ns)) == 0
	})
	eventually(t, 90*time.Second, "every Bedrock object is gone", func() bool {
		for _, obj := range []client.Object{network, def, other, web, eip1, eip2, lb, zone} {
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	})
}

func leftovers(t *testing.T, c client.Client, namespace string) []string {
	t.Helper()
	var left []string
	for _, gvk := range purgedFabricKinds {
		items, err := listObjects(context.Background(), c, gvk, client.MatchingLabels{LabelManaged: "true"})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			underlay := item.GetLabels()[LabelComponent] == ComponentUnderlay
			namespaceDefault := item.GetName() == DefaultSecurityGroupName(namespace)
			if !underlay && !namespaceDefault {
				left = append(left, gvk.Kind+"/"+item.GetName())
			}
		}
	}
	return left
}

func TestManagerSweepsOrphansAfterAForcedFinalizerRemoval(t *testing.T) {
	c, ctx := startChain(t)
	if err := c.Get(ctx, client.ObjectKey{Name: "network.provider-networks"}, &v1alpha1.Setting{}); apierrors.IsNotFound(err) {
		putSetting(t, c, "network.provider-networks", `[{"name":"public","interface":"bond0","cidr":"192.0.2.0/24","gateway":"192.0.2.1"}]`)
	}
	ns := newNamespaceIn(t, c, "sweep", "net")
	eip := elasticIPOf(ns, "orphan", "")
	requireAccepted(t, c, eip)
	eventually(t, 60*time.Second, "the ElasticIP is reserved", func() bool { return isReady(c, eip) })
	name := FabricName(ns, "orphan")
	if _, err := getObject(ctx, c, fabric.OvnEipGVK, name); err != nil {
		t.Fatal(err)
	}
	eip.SetFinalizers(nil)
	if err := c.Update(ctx, eip); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, eip); err != nil {
		t.Fatal(err)
	}
	sweeper := &Sweeper{Client: c, Interval: time.Second, MinAge: time.Second}
	go func() { _ = sweeper.Start(ctx) }()
	eventually(t, 30*time.Second, "the OvnEip is swept", func() bool {
		_, err := getObject(ctx, c, fabric.OvnEipGVK, name)
		return apierrors.IsNotFound(err)
	})
}

func ensureProviderSetting(t *testing.T, c client.Client, ctx context.Context) {
	t.Helper()
	if err := c.Get(ctx, client.ObjectKey{Name: "network.provider-networks"}, &v1alpha1.Setting{}); apierrors.IsNotFound(err) {
		putSetting(t, c, "network.provider-networks", `[{"name":"public","interface":"bond0","cidr":"192.0.2.0/24","gateway":"192.0.2.1"}]`)
	}
}

func reasonOf(c client.Client, eip *v1alpha1.ElasticIP) string {
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(eip), eip); err != nil {
		return ""
	}
	if cond := meta.FindStatusCondition(eip.Status.Conditions, v1alpha1.ConditionReady); cond != nil {
		return cond.Reason
	}
	return ""
}

func TestManagerRetriesElasticIPsWhenTheHolderIsDeleted(t *testing.T) {
	c, ctx := startChain(t)
	ensureProviderSetting(t, c, ctx)
	older, younger := newNamespaceIn(t, c, "waitaddr", "aa"), newNamespaceIn(t, c, "waitaddr", "bb")
	holder := elasticIPOf(older, "holder", "192.0.2.80")
	waiter := elasticIPOf(younger, "waiter", "192.0.2.80")
	requireAccepted(t, c, holder)
	eventually(t, 60*time.Second, "the holder is reserved", func() bool { return isReady(c, holder) })
	requireAccepted(t, c, waiter)
	eventually(t, 60*time.Second, "the waiter reports AddressInUse", func() bool { return reasonOf(c, waiter) == ReasonAddressInUse })
	if err := c.Delete(ctx, holder); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the waiter takes the address", func() bool { return isReady(c, waiter) })
}

func TestManagerRetriesElasticIPsWhenTheTargetHolderIsDeleted(t *testing.T) {
	c, ctx := startChain(t)
	ensureProviderSetting(t, c, ctx)
	ns := newNamespaceIn(t, c, "waittarget", "net")
	requireAccepted(t, c, virtualMachineUsing(ns, "vm1"))
	holder, waiter := elasticIPOf(ns, "a-holder", ""), elasticIPOf(ns, "b-waiter", "")
	holder.Spec.Target, waiter.Spec.Target = vmTarget("vm1"), vmTarget("vm1")
	requireAccepted(t, c, holder)
	eventually(t, 60*time.Second, "the holder is processed", func() bool { return reasonOf(c, holder) != "" && reasonOf(c, holder) != ReasonAllocating })
	requireAccepted(t, c, waiter)
	eventually(t, 60*time.Second, "the waiter reports TargetInUse", func() bool { return reasonOf(c, waiter) == ReasonTargetInUse })
	if err := c.Delete(ctx, holder); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, "the waiter leaves TargetInUse", func() bool {
		reason := reasonOf(c, waiter)
		return reason != "" && reason != ReasonTargetInUse
	})
}

func fabricSubnetMTU(c client.Client, namespace, name string) int64 {
	fabricSubnet, err := fabricSubnetOf(c, namespace, name)
	if err != nil {
		return 0
	}
	mtu, _, _ := unstructured.NestedInt64(fabricSubnet.Object, "spec", "mtu")
	return mtu
}

func TestManagerAppliesAnMTUSettingChangeToAnExistingSubnet(t *testing.T) {
	c, ctx := startChain(t)
	ns := newNamespaceIn(t, c, "mtuchain", "net")
	requireAccepted(t, c, networkOf("mtuchain", "mtuchain", "10.100.0.0/16"))
	subnet := policySubnet(ns, "default", "mtuchain", "10.100.1.0/24")
	requireAccepted(t, c, subnet)
	eventually(t, 60*time.Second, "the Subnet is ready without an MTU", func() bool {
		_, err := fabricSubnetOf(c, ns, "default")
		return isReady(c, subnet) && err == nil && fabricSubnetMTU(c, ns, "default") == 0
	})
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.mtu"}, Spec: v1alpha1.SettingSpec{Value: "1400"}}
	requireAccepted(t, c, setting)
	t.Cleanup(func() { _ = c.Delete(context.Background(), setting) })
	eventually(t, 30*time.Second, "the Fabric Subnet follows the Setting", func() bool {
		return fabricSubnetMTU(c, ns, "default") == 1400
	})
	if err := c.Get(ctx, client.ObjectKeyFromObject(subnet), subnet); err != nil || subnet.Generation != 1 {
		t.Fatalf("the Subnet must stay untouched: generation %d, error %v", subnet.Generation, err)
	}
}
