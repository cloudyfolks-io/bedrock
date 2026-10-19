package fabric

import (
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func text(u *unstructured.Unstructured, path ...string) string {
	value, _, _ := unstructured.NestedString(u.Object, path...)
	return value
}

func flag(u *unstructured.Unstructured, path ...string) bool {
	value, _, _ := unstructured.NestedBool(u.Object, path...)
	return value
}

func list(u *unstructured.Unstructured, path ...string) []string {
	value, _, _ := unstructured.NestedStringSlice(u.Object, path...)
	return value
}

func condition(u *unstructured.Unstructured, conditionType string) (status, message string) {
	items, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok || entry["type"] != conditionType {
			continue
		}
		status, _ = entry["status"].(string)
		message, _ = entry["message"].(string)
		return status, message
	}
	return "", ""
}

func VpcStandby(u *unstructured.Unstructured) bool {
	return flag(u, "status", "standby")
}

func VpcExternalSubnets(u *unstructured.Unstructured) []string {
	return list(u, "status", "extraExternalSubnets")
}

func VpcSpecExternalSubnets(u *unstructured.Unstructured) []string {
	return list(u, "spec", "extraExternalSubnets")
}

func VpcDefaultNextHop(u *unstructured.Unstructured) string {
	routes, _, _ := unstructured.NestedSlice(u.Object, "spec", "staticRoutes")
	for _, item := range routes {
		route, ok := item.(map[string]any)
		if ok && route["cidr"] == "0.0.0.0/0" {
			hop, _ := route["nextHopIP"].(string)
			return hop
		}
	}
	return ""
}

func VpcSubnets(u *unstructured.Unstructured) []string {
	return list(u, "status", "subnets")
}

func EipAddress(u *unstructured.Unstructured) string {
	return text(u, "status", "v4Ip")
}

func EipReady(u *unstructured.Unstructured) bool {
	return flag(u, "status", "ready")
}

func FipReady(u *unstructured.Unstructured) bool {
	return flag(u, "status", "ready")
}

func FipIPName(u *unstructured.Unstructured) string {
	return text(u, "spec", "ipName")
}

func SnatRuleReady(u *unstructured.Unstructured) bool {
	return flag(u, "status", "ready")
}

func RouterLBRuleService(u *unstructured.Unstructured) string {
	return text(u, "status", "service")
}

func SubnetReady(u *unstructured.Unstructured) bool {
	status, _ := condition(u, "Ready")
	return status == "True"
}

func SubnetErrorMessage(u *unstructured.Unstructured) string {
	status, message := condition(u, "Ready")
	if status == "False" {
		return message
	}
	return ""
}

func SubnetUsedAddresses(u *unstructured.Unstructured) int64 {
	return statusNumber(u, "v4usingIPs")
}

func SubnetAvailableAddresses(u *unstructured.Unstructured) int64 {
	return statusNumber(u, "v4availableIPs")
}

func statusNumber(u *unstructured.Unstructured, field string) int64 {
	value, found, _ := unstructured.NestedFieldNoCopy(u.Object, "status", field)
	if !found {
		return 0
	}
	switch n := value.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case string:
		parsed, _ := strconv.ParseInt(n, 10, 64)
		return parsed
	}
	return 0
}

func DNSZoneReady(u *unstructured.Unstructured) bool {
	status, _ := condition(u, "Ready")
	return status == "True"
}

func IPAddress(u *unstructured.Unstructured) string {
	return text(u, "spec", "v4IpAddress")
}

func IPOwner(u *unstructured.Unstructured) (namespace, podName, subnet string) {
	return text(u, "spec", "namespace"), text(u, "spec", "podName"), text(u, "spec", "subnet")
}

func SecurityGroupProgrammed(u *unstructured.Unstructured) bool {
	return text(u, "status", "portGroup") != ""
}

func EipFailed(u *unstructured.Unstructured) bool {
	status, _ := condition(u, "Ready")
	return status == "False" && EipAddress(u) == ""
}

func VipAddress(u *unstructured.Unstructured) string {
	return text(u, "status", "v4ip")
}

func SwitchLBRuleService(u *unstructured.Unstructured) string {
	return text(u, "status", "service")
}
