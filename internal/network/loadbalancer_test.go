package network

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func listenerSpec(port int32, protocol string, targets ...v1alpha1.LoadBalancerTarget) v1alpha1.LoadBalancerListener {
	return v1alpha1.LoadBalancerListener{Port: port, Protocol: protocol, Targets: targets}
}

func target(vm string, port int32) v1alpha1.LoadBalancerTarget {
	return v1alpha1.LoadBalancerTarget{VMRef: vm, Port: port}
}

func TestBuildListenersUppercasesProtocolAndDefaultsToTCP(t *testing.T) {
	got, _, v := BuildListeners([]v1alpha1.LoadBalancerListener{listenerSpec(80, "", target("a", 8080)), listenerSpec(53, "udp", target("a", 5353))}, map[string][]string{"a": {"10.0.0.2"}})
	if v != nil || len(got) != 2 || got[0].Protocol != "TCP" || got[1].Protocol != "UDP" {
		t.Fatalf("listeners %+v violation %+v", got, v)
	}
	if got[0].TargetPort != 8080 || !slices.Equal(got[0].Endpoints, []string{"10.0.0.2"}) {
		t.Fatalf("listener %+v", got[0])
	}
}

func TestBuildListenersRequireOneTargetPort(t *testing.T) {
	_, _, v := BuildListeners([]v1alpha1.LoadBalancerListener{listenerSpec(80, "TCP", target("a", 8080), target("b", 9090))}, map[string][]string{"a": {"10.0.0.2"}, "b": {"10.0.0.3"}})
	if v == nil || v.Reason != ReasonTargetPortsDiffer {
		t.Fatalf("violation %+v", v)
	}
}

func TestBuildListenersCollectsMissingAddresses(t *testing.T) {
	got, missing, v := BuildListeners([]v1alpha1.LoadBalancerListener{listenerSpec(80, "TCP", target("a", 80), target("b", 80)), listenerSpec(81, "TCP", target("b", 81))}, map[string][]string{"a": {"10.0.0.2"}})
	if v != nil || !slices.Equal(missing, []string{"b"}) || len(got[0].Endpoints) != 1 || len(got[1].Endpoints) != 0 {
		t.Fatalf("listeners %+v missing %v violation %+v", got, missing, v)
	}
}

func TestBuildListenersMapsHealthCheck(t *testing.T) {
	with := listenerSpec(80, "TCP", target("a", 80))
	with.HealthCheck = &v1alpha1.LoadBalancerHealthCheck{}
	got, _, _ := BuildListeners([]v1alpha1.LoadBalancerListener{with, listenerSpec(81, "TCP", target("a", 81))}, map[string][]string{"a": {"10.0.0.2"}})
	if !got[0].HealthCheck || got[1].HealthCheck {
		t.Fatalf("listeners %+v", got)
	}
}

func listenerInput(listeners ...Listener) ListenerInput {
	return ListenerInput{Namespace: "ns", Name: "lb", EIP: "eip", Vpc: "acme", Labels: map[string]string{"l": "v"}, Annotations: map[string]string{"a": "v"}, Listeners: listeners}
}

func TestListenerRulesOnePerListener(t *testing.T) {
	rules := ListenerRules(listenerInput(
		Listener{Port: 80, Protocol: "TCP", TargetPort: 8080, Endpoints: []string{"10.0.0.2"}},
		Listener{Port: 80, Protocol: "UDP", TargetPort: 8080, Endpoints: []string{"10.0.0.2"}},
	))
	if len(rules) != 2 || rules[0].GetName() != ListenerRuleName("ns", "lb", 80, "TCP") || rules[0].GetName() == rules[1].GetName() {
		t.Fatalf("rules %v", rules)
	}
	spec, _, _ := unstructured.NestedMap(rules[0].Object, "spec")
	if spec["ovnEip"] != "eip" || spec["vpc"] != "acme" || spec["namespace"] != "ns" {
		t.Fatalf("spec %v", spec)
	}
	ports, _ := spec["ports"].([]any)
	port := ports[0].(map[string]any)
	if len(ports) != 1 || port["name"] != "tcp-80" || port["port"] != int64(80) || port["targetPort"] != int64(8080) || port["protocol"] != "TCP" {
		t.Fatalf("ports %v", ports)
	}
	if rules[0].GetLabels()["l"] != "v" || rules[0].GetAnnotations()["a"] != "v" {
		t.Fatal("labels and annotations must be carried")
	}
}

func TestListenerRulesSortEndpoints(t *testing.T) {
	rules := ListenerRules(listenerInput(Listener{Port: 80, Protocol: "TCP", TargetPort: 80, Endpoints: []string{"10.0.0.9", "10.0.0.10", "10.0.0.2"}}))
	endpoints, _, _ := unstructured.NestedStringSlice(rules[0].Object, "spec", "endpoints")
	if !slices.IsSorted(endpoints) || len(endpoints) != 3 {
		t.Fatalf("endpoints %v", endpoints)
	}
}

func TestListenerRulesHealthCheckAnnotation(t *testing.T) {
	rules := ListenerRules(listenerInput(
		Listener{Port: 80, Protocol: "TCP", TargetPort: 80, Endpoints: []string{"10.0.0.2"}, HealthCheck: true},
		Listener{Port: 81, Protocol: "TCP", TargetPort: 81, Endpoints: []string{"10.0.0.2"}},
	))
	if _, found := rules[0].GetAnnotations()[fabric.AnnotationServiceHealthCheck]; found {
		t.Fatal("health checks stay on by default")
	}
	if rules[1].GetAnnotations()[fabric.AnnotationServiceHealthCheck] != "false" {
		t.Fatalf("annotations %v", rules[1].GetAnnotations())
	}
}

func TestKindsFromMapper(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "fabric.cloudyfolks.io", Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Group: "fabric.cloudyfolks.io", Version: "v1", Kind: "RouterLBRule"}, meta.RESTScopeRoot)
	known := KindsFromMapper(mapper)
	if !known(schema.GroupKind{Group: "fabric.cloudyfolks.io", Kind: "RouterLBRule"}) || known(schema.GroupKind{Group: "fabric.cloudyfolks.io", Kind: "DNSZone"}) {
		t.Fatal("unexpected kind knowledge")
	}
}

func TestUnsupportedKindMessage(t *testing.T) {
	v := UnsupportedKind("RouterLBRule")
	if v.Reason != ReasonUnsupported || v.Message != "the Fabric kind RouterLBRule is not installed; upgrade the fabric component" {
		t.Fatalf("violation %+v", v)
	}
}

func TestInternalRulesOnePerListener(t *testing.T) {
	in := InternalInput{Namespace: "ns", Name: "lb", Vip: "10.0.0.200", Labels: map[string]string{"l": "v"}, Annotations: map[string]string{"a": "v"}, Listeners: []Listener{
		{Port: 80, Protocol: "TCP", TargetPort: 8080, Endpoints: []string{"10.0.0.9", "10.0.0.10"}},
		{Port: 53, Protocol: "UDP", TargetPort: 5353, Endpoints: []string{"10.0.0.2"}},
	}}
	rules := InternalRules(in)
	if len(rules) != 2 || rules[0].GetKind() != "SwitchLBRule" || rules[0].GetName() != ListenerRuleName("ns", "lb", 80, "TCP") {
		t.Fatalf("rules %v", rules)
	}
	spec, _, _ := unstructured.NestedMap(rules[0].Object, "spec")
	endpoints, _, _ := unstructured.NestedStringSlice(rules[0].Object, "spec", "endpoints")
	if spec["vip"] != "10.0.0.200" || spec["namespace"] != "ns" || !slices.IsSorted(endpoints) || len(endpoints) != 2 {
		t.Fatalf("spec %v", spec)
	}
	if rules[0].GetLabels()["l"] != "v" || rules[0].GetAnnotations()["a"] != "v" {
		t.Fatal("labels and annotations must be carried")
	}
}

func TestInternalVipUsesTheLoadBalancerIdentity(t *testing.T) {
	vip := InternalVip(InternalInput{Namespace: "ns", Name: "lb", Labels: map[string]string{"l": "v"}}, "net-lb")
	if vip.GetName() != InternalVipName("ns", "lb") || vip.GetKind() != "Vip" {
		t.Fatalf("vip %v", vip.Object)
	}
	if subnet, _, _ := unstructured.NestedString(vip.Object, "spec", "subnet"); subnet != "net-lb" {
		t.Fatalf("subnet %q", subnet)
	}
	if _, found, _ := unstructured.NestedString(vip.Object, "spec", "type"); found {
		t.Fatalf("a plane Vip has no type: %v", vip.Object)
	}
}
