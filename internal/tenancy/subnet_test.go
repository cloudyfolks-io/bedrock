package tenancy

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func subnetIn(name, namespace, network, cidr string) *v1alpha1.Subnet {
	return &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: network, CIDR: cidr, DHCP: true},
	}
}

func projectWithSubnet(name, tenant, cidr string) v1alpha1.Project {
	project := *projectFor(name, tenant)
	project.Status.Subnet = cidr
	return project
}

func TestUsedCIDRsIncludeClaimedAndExistingSubnets(t *testing.T) {
	subnets := []v1alpha1.Subnet{
		*subnetIn("s1", "a", "acme", "10.100.0.0/24"),
		*subnetIn("s2", "b", "other", "10.100.1.0/24"),
		*subnetIn("s3", "c", "acme", "garbage"),
	}
	projects := []v1alpha1.Project{projectWithSubnet("p1", "acme", "10.100.2.0/24"), projectWithSubnet("p2", "other", "10.100.3.0/24")}
	want := []netip.Prefix{netip.MustParsePrefix("10.100.0.0/24"), netip.MustParsePrefix("10.100.2.0/24")}
	if got := usedCIDRs("acme", subnets, projects); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSubnetStep(t *testing.T) {
	ready := subnetIn("default", "acme-shop", "acme", "10.100.0.0/24")
	ready.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready"}}
	waiting := subnetIn("default", "acme-shop", "acme", "10.100.0.0/24")
	waiting.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Pending", Message: "waiting for fabric"}}
	cases := []struct {
		name   string
		subnet *v1alpha1.Subnet
		err    error
		want   Step
	}{
		{"apply error", nil, errors.New("boom"), Step{Reason: ReasonApplyFailed, Message: "boom"}},
		{"ready", ready, nil, Step{Ready: true, Reason: ReasonReady}},
		{"waiting with message", waiting, nil, Step{Reason: ReasonWaitingForSubnet, Message: "waiting for fabric"}},
		{"no status", subnetIn("default", "acme-shop", "acme", "10.100.0.0/24"), nil, Step{Reason: ReasonWaitingForSubnet, Message: "subnet has no status yet"}},
		{"missing", nil, nil, Step{Reason: ReasonWaitingForSubnet, Message: "subnet not found yet"}},
	}
	for _, c := range cases {
		if got := subnetStep(c.subnet, c.err); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func subnetOf(t *testing.T, c client.Client, namespace string) *v1alpha1.Subnet {
	t.Helper()
	subnet := &v1alpha1.Subnet{}
	if c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "default"}, subnet) != nil {
		return nil
	}
	return subnet
}

func TestProjectAllocatesDistinctSubnets(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("web", "acme"), projectFor("data", "acme"))
	cidrs := map[string]string{}
	for _, name := range []string{"web", "data"} {
		projectReady(t, c, name, func(p *v1alpha1.Project) bool { return p.Status.Subnet != "" })
		cidrs[name] = fetchProject(t, c, name).Status.Subnet
		waitFor(t, func() bool { return subnetOf(t, c, "acme-"+name) != nil })
		subnet := subnetOf(t, c, "acme-"+name)
		if subnet.Spec.NetworkRef != "acme" || subnet.Spec.CIDR != cidrs[name] || controllerReferenceUID(subnet.OwnerReferences, "Project") != string(fetchProject(t, c, name).UID) {
			t.Fatalf("subnet of %s = %+v", name, subnet)
		}
	}
	got := map[string]bool{cidrs["web"]: true, cidrs["data"]: true}
	if !reflect.DeepEqual(got, map[string]bool{"10.100.0.0/24": true, "10.100.1.0/24": true}) {
		t.Fatalf("cidrs = %v", cidrs)
	}
}

func TestProjectSkipsCIDRsOfOtherSubnets(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, namespaceNamed("team", nil), subnetIn("lab", "team", "acme", "10.100.0.0/23"), subnetIn("x", "team", "other", "10.100.2.0/24"))
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("web", "acme"))
	projectReady(t, c, "web", func(p *v1alpha1.Project) bool { return p.Status.Subnet != "" })
	if got := fetchProject(t, c, "web").Status.Subnet; got != "10.100.2.0/24" {
		t.Fatalf("subnet = %s", got)
	}
}

func TestProjectKeepsItsSubnetAcrossReconciles(t *testing.T) {
	c, _ := startEnv(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("web", "acme"))
	reconciler := &ProjectReconciler{Client: c, Reader: c}
	request := reconcile.Request{NamespacedName: client.ObjectKey{Name: "web"}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first := subnetOf(t, c, "acme-web")
	cidr := fetchProject(t, c, "web").Status.Subnet
	for range 2 {
		if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	again := subnetOf(t, c, "acme-web")
	if fetchProject(t, c, "web").Status.Subnet != cidr || first == nil || again.ResourceVersion != first.ResourceVersion {
		t.Fatalf("subnet changed: %v -> %v", first, again)
	}
}

func TestProjectClaimsTheCIDRBeforeTheSubnet(t *testing.T) {
	admin, cfg := startEnv(t)
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: newScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	refusing := interceptor.NewClient(watching, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			if kinded, ok := obj.(interface{ GetKind() string }); ok && kinded.GetKind() == "Subnet" {
				return errors.New("subnet apply refused")
			}
			return c.Apply(ctx, obj, opts...)
		},
	})
	mustCreate(t, admin, tenantWith("acme", "10.100.0.0/16"), projectFor("web", "acme"))
	reconciler := &ProjectReconciler{Client: refusing, Reader: admin}
	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKey{Name: "web"}}); err == nil {
		t.Fatal("the failed apply was not reported")
	}
	project := fetchProject(t, admin, "web")
	if project.Status.Subnet != "10.100.0.0/24" {
		t.Fatalf("subnet = %q", project.Status.Subnet)
	}
	requireCondition(t, project.Status.Conditions, ConditionSubnetReady, metav1.ConditionFalse, ReasonApplyFailed)
}

func TestProjectReportsAnExhaustedVPC(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/24"), projectFor("a-first", "acme"))
	projectReady(t, c, "a-first", func(p *v1alpha1.Project) bool { return p.Status.Subnet == "10.100.0.0/24" })
	mustCreate(t, c, projectFor("b-second", "acme"))
	projectReady(t, c, "b-second", hasCondition(ConditionSubnetReady, metav1.ConditionFalse, ReasonCIDRExhausted))
	projectReady(t, c, "b-second", hasCondition(v1alpha1.ConditionDegraded, metav1.ConditionTrue, ""))
	if got := fetchProject(t, c, "b-second").Status.Subnet; got != "" {
		t.Fatalf("subnet = %q", got)
	}
}

func setSubnetReady(t *testing.T, c client.Client, namespace string, status metav1.ConditionStatus, message string) {
	t.Helper()
	waitFor(t, func() bool {
		subnet := subnetOf(t, c, namespace)
		if subnet == nil {
			return false
		}
		subnet.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: status, Reason: "Test", Message: message, LastTransitionTime: metav1.Now()}}
		return c.Status().Update(context.Background(), subnet) == nil
	})
}

func TestProjectMirrorsTheSubnetCondition(t *testing.T) {
	c, _ := projectWorld(t)
	mustCreate(t, c, tenantWith("acme", "10.100.0.0/16"), projectFor("web", "acme"))
	setSubnetReady(t, c, "acme-web", metav1.ConditionFalse, "waiting")
	projectReady(t, c, "web", hasCondition(ConditionSubnetReady, metav1.ConditionFalse, ReasonWaitingForSubnet))
	projectReady(t, c, "web", hasCondition(v1alpha1.ConditionProgressing, metav1.ConditionTrue, ""))
	setSubnetReady(t, c, "acme-web", metav1.ConditionTrue, "")
	projectReady(t, c, "web", hasCondition(ConditionSubnetReady, metav1.ConditionTrue, ReasonReady))
}
