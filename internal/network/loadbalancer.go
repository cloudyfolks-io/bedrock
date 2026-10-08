package network

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

type Listener struct {
	Port        int32
	Protocol    string
	TargetPort  int32
	Endpoints   []string
	HealthCheck bool
}

type ListenerInput struct {
	Namespace   string
	Name        string
	EIP         string
	Vpc         string
	Labels      map[string]string
	Annotations map[string]string
	Listeners   []Listener
}

func listenerProtocol(spec v1alpha1.LoadBalancerListener) string {
	if spec.Protocol == "" {
		return v1alpha1.LoadBalancerProtocolTCP
	}
	return strings.ToUpper(spec.Protocol)
}

func BuildListeners(spec []v1alpha1.LoadBalancerListener, addresses map[string]string) ([]Listener, []string, *Violation) {
	listeners := make([]Listener, 0, len(spec))
	var missing []string
	for _, s := range spec {
		listener := Listener{Port: s.Port, Protocol: listenerProtocol(s), HealthCheck: s.HealthCheck != nil}
		for i, t := range s.Targets {
			if i == 0 {
				listener.TargetPort = t.Port
			}
			if t.Port != listener.TargetPort {
				return nil, nil, &Violation{Reason: ReasonTargetPortsDiffer, Message: fmt.Sprintf("the listener %d/%s needs one port for all targets", s.Port, listener.Protocol)}
			}
			address, found := addresses[t.VMRef]
			if !found {
				missing = append(missing, t.VMRef)
				continue
			}
			listener.Endpoints = append(listener.Endpoints, address)
		}
		listeners = append(listeners, listener)
	}
	slices.Sort(missing)
	return listeners, slices.Compact(missing), nil
}

func ListenerRules(in ListenerInput) []*unstructured.Unstructured {
	rules := make([]*unstructured.Unstructured, len(in.Listeners))
	for i, l := range in.Listeners {
		meta := fabric.Meta{Name: ListenerRuleName(in.Namespace, in.Name, l.Port, l.Protocol), Labels: maps.Clone(in.Labels), Annotations: maps.Clone(in.Annotations)}
		ports := []fabric.RouterLBPort{{Name: strings.ToLower(l.Protocol) + "-" + fmt.Sprint(l.Port), Port: l.Port, TargetPort: l.TargetPort, Protocol: l.Protocol}}
		endpoints := slices.Sorted(slices.Values(l.Endpoints))
		rules[i] = routerRule(l, meta, in, endpoints, ports)
	}
	return rules
}

func routerRule(l Listener, meta fabric.Meta, in ListenerInput, endpoints []string, ports []fabric.RouterLBPort) *unstructured.Unstructured {
	if l.HealthCheck {
		return fabric.RouterLBRule(meta, in.EIP, in.Vpc, in.Namespace, endpoints, ports)
	}
	return fabric.RouterLBRuleWithoutHealthCheck(meta, in.EIP, in.Vpc, in.Namespace, endpoints, ports)
}

type InternalInput struct {
	Namespace   string
	Name        string
	Vip         string
	Labels      map[string]string
	Annotations map[string]string
	Listeners   []Listener
}

func InternalRules(in InternalInput) []*unstructured.Unstructured {
	rules := make([]*unstructured.Unstructured, len(in.Listeners))
	for i, l := range in.Listeners {
		meta := fabric.Meta{Name: ListenerRuleName(in.Namespace, in.Name, l.Port, l.Protocol), Labels: maps.Clone(in.Labels), Annotations: maps.Clone(in.Annotations)}
		ports := []fabric.RouterLBPort{{Name: strings.ToLower(l.Protocol) + "-" + fmt.Sprint(l.Port), Port: l.Port, TargetPort: l.TargetPort, Protocol: l.Protocol}}
		rules[i] = fabric.SwitchLBRule(meta, in.Vip, in.Namespace, slices.Sorted(slices.Values(l.Endpoints)), ports)
	}
	return rules
}

func InternalVip(in InternalInput, subnet string) *unstructured.Unstructured {
	return fabric.Vip(fabric.Meta{Name: FabricName(in.Namespace, in.Name), Labels: maps.Clone(in.Labels), Annotations: maps.Clone(in.Annotations)}, in.Namespace, subnet)
}
