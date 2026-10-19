package fabrictest

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const labelSubnet = "fabric.cloudyfolks.io/subnet"

func readyCondition() []any {
	return []any{map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "ready", "lastTransitionTime": "2026-01-01T00:00:00Z"}}
}

func updateStatus(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, name string, mutate func(status map[string]any, spec map[string]any)) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKey{Name: name}, u); err != nil {
		return err
	}
	status, _, _ := unstructured.NestedMap(u.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	mutate(status, spec)
	u.Object["status"] = status
	return c.Status().Update(ctx, u)
}

func MarkVpcStandby(ctx context.Context, c client.Client, name string) error {
	return updateStatus(ctx, c, fabric.VpcGVK, name, func(status, spec map[string]any) {
		status["standby"] = true
		status["enableExternal"] = spec["enableExternal"] == true
		if subnets, ok := spec["extraExternalSubnets"]; ok {
			status["extraExternalSubnets"] = subnets
		}
	})
}

func MarkSubnetReady(ctx context.Context, c client.Client, name string) error {
	return updateStatus(ctx, c, fabric.SubnetGVK, name, func(status, _ map[string]any) {
		status["conditions"] = readyCondition()
	})
}

func SetSubnetUsedAddresses(ctx context.Context, c client.Client, name string, used int64) error {
	return updateStatus(ctx, c, fabric.SubnetGVK, name, func(status, _ map[string]any) {
		status["v4usingIPs"] = used
	})
}

func SetSubnetCapacity(ctx context.Context, c client.Client, name string, using, available int64) error {
	return updateStatus(ctx, c, fabric.SubnetGVK, name, func(status, _ map[string]any) {
		status["v4usingIPs"] = using
		status["v4availableIPs"] = available
	})
}

func MarkEipAllocated(ctx context.Context, c client.Client, name, address string) error {
	return updateStatus(ctx, c, fabric.OvnEipGVK, name, func(status, _ map[string]any) {
		status["v4Ip"] = address
		status["ready"] = true
	})
}

func MarkEipRefused(ctx context.Context, c client.Client, name, message string) error {
	return updateStatus(ctx, c, fabric.OvnEipGVK, name, func(status, _ map[string]any) {
		status["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "reason": "Refused", "message": message, "lastTransitionTime": "2026-01-01T00:00:00Z"}}
	})
}

func MarkVipAllocated(ctx context.Context, c client.Client, name, address string) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(fabric.VipGVK)
	if err := c.Get(ctx, client.ObjectKey{Name: name}, u); err != nil {
		return err
	}
	u.Object["status"] = map[string]any{"v4ip": address}
	return c.Update(ctx, u)
}

func MarkSwitchLBRuleProgrammed(ctx context.Context, c client.Client, name, service string) error {
	return updateStatus(ctx, c, fabric.SwitchLBRuleGVK, name, func(status, _ map[string]any) {
		status["service"] = service
	})
}

func MarkFipReady(ctx context.Context, c client.Client, name string) error {
	return updateStatus(ctx, c, fabric.OvnFipGVK, name, func(status, _ map[string]any) {
		status["ready"] = true
	})
}

func MarkRouterLBRuleProgrammed(ctx context.Context, c client.Client, name, service string) error {
	return updateStatus(ctx, c, fabric.RouterLBRuleGVK, name, func(status, _ map[string]any) {
		status["service"] = service
	})
}

func MarkDNSZoneReady(ctx context.Context, c client.Client, name string) error {
	return updateStatus(ctx, c, fabric.DNSZoneGVK, name, func(status, _ map[string]any) {
		status["conditions"] = readyCondition()
		status["activeRecords"] = int64(1)
	})
}

func MarkSecurityGroupProgrammed(ctx context.Context, c client.Client, name string) error {
	return updateStatus(ctx, c, fabric.SecurityGroupGVK, name, func(status, _ map[string]any) {
		status["portGroup"] = "pg-" + name
	})
}

func MarkProviderNetworkReady(ctx context.Context, c client.Client, name string) error {
	return updateStatus(ctx, c, fabric.ProviderNetworkGVK, name, func(status, _ map[string]any) {
		status["ready"] = true
	})
}

func CreateRouterPort(ctx context.Context, c client.Client, vpc, externalSubnet, address string) error {
	name := vpc + "-" + externalSubnet
	eip := fabric.OvnEip(fabric.Meta{Name: name}, externalSubnet, "")
	if err := c.Create(ctx, eip); err != nil {
		return err
	}
	return MarkEipAllocated(ctx, c, name, address)
}

func CreateIP(ctx context.Context, c client.Client, name, namespace, podName, subnet, address string) error {
	ip := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"podName": podName, "namespace": namespace, "subnet": subnet, "v4IpAddress": address, "ipAddress": address,
	}}}
	ip.SetGroupVersionKind(fabric.IPGVK)
	ip.SetName(name)
	ip.SetLabels(map[string]string{labelSubnet: subnet})
	return c.Create(ctx, ip)
}
