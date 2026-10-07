package operator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestSubnetValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	subnet := &v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.1.0/24", Gateway: "10.100.1.1", DHCP: true, ExcludeIPs: []string{"10.100.1.2..10.100.1.10"}},
	}
	createValid(t, c, subnet)
	if err := c.Status().Update(ctx, subnet); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	withoutDHCP := &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "static", Namespace: "default"}, Spec: v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.2.0/24"}}
	createValid(t, c, withoutDHCP)
	var fresh v1alpha1.Subnet
	if err := c.Get(ctx, client.ObjectKeyFromObject(withoutDHCP), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.DHCP {
		t.Fatal("dhcp false must persist")
	}

	subnet.Spec.CIDR = "10.100.9.0/24"
	updateInvalid(t, c, subnet, "cidr is immutable")
	if err := c.Get(ctx, client.ObjectKeyFromObject(subnet), subnet); err != nil {
		t.Fatal(err)
	}
	subnet.Spec.NetworkRef = "other"
	updateInvalid(t, c, subnet, "networkRef is immutable")

	createInvalid(t, c, &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "bad-cidr", Namespace: "default"}, Spec: v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.1.0"}}, "cidr must be a CIDR")
	createInvalid(t, c, &v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "bad-gateway", Namespace: "default"}, Spec: v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.3.0/24", Gateway: "gw"}}, "gateway must be an IP address")
}

func TestElasticIPAndLoadBalancerValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	reserved := &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Name: "reserved", Namespace: "default"}, Spec: v1alpha1.ElasticIPSpec{ProviderNetwork: "public"}}
	createValid(t, c, reserved)
	reserved.Status.Address = "203.0.113.10"
	if err := c.Status().Update(ctx, reserved); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	attached := &v1alpha1.ElasticIP{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       v1alpha1.ElasticIPSpec{ProviderNetwork: "public", Address: "203.0.113.11", Target: &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetVirtualMachine, Name: "web", SubnetRef: "default"}},
	}
	createValid(t, c, attached)

	badKind := &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Name: "bad-kind", Namespace: "default"}, Spec: v1alpha1.ElasticIPSpec{ProviderNetwork: "public", Target: &v1alpha1.ElasticIPTarget{Kind: "Pod", Name: "x"}}}
	createInvalid(t, c, badKind, "Unsupported value")
	badSubnetRef := &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Name: "bad-subnet-ref", Namespace: "default"}, Spec: v1alpha1.ElasticIPSpec{ProviderNetwork: "public", Target: &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetLoadBalancer, Name: "lb", SubnetRef: "default"}}}
	createInvalid(t, c, badSubnetRef, "subnetRef selects a VirtualMachine interface")
	badAddress := &v1alpha1.ElasticIP{ObjectMeta: metav1.ObjectMeta{Name: "bad-address", Namespace: "default"}, Spec: v1alpha1.ElasticIPSpec{ProviderNetwork: "public", Address: "nope"}}
	createInvalid(t, c, badAddress, "address must be an IP address")

	balancer := &v1alpha1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: v1alpha1.LoadBalancerSpec{
			ElasticIPRef: "web",
			Listeners: []v1alpha1.LoadBalancerListener{{
				Port: 443, Protocol: v1alpha1.LoadBalancerProtocolTCP,
				Targets:     []v1alpha1.LoadBalancerTarget{{VMRef: "web", Port: 8443}},
				HealthCheck: &v1alpha1.LoadBalancerHealthCheck{},
			}},
		},
	}
	createValid(t, c, balancer)
	if balancer.Spec.Listeners[0].HealthCheck.IntervalSeconds != 5 {
		t.Fatalf("health check defaults missing: %+v", balancer.Spec.Listeners[0].HealthCheck)
	}

	badProtocol := &v1alpha1.LoadBalancer{ObjectMeta: metav1.ObjectMeta{Name: "bad-protocol", Namespace: "default"}, Spec: v1alpha1.LoadBalancerSpec{Listeners: []v1alpha1.LoadBalancerListener{{Port: 53, Protocol: "SCTP"}}}}
	createInvalid(t, c, badProtocol, "Unsupported value")
	badPort := &v1alpha1.LoadBalancer{ObjectMeta: metav1.ObjectMeta{Name: "bad-port", Namespace: "default"}, Spec: v1alpha1.LoadBalancerSpec{Listeners: []v1alpha1.LoadBalancerListener{{Port: 70000, Protocol: "TCP"}}}}
	createInvalid(t, c, badPort, "spec.listeners[0].port")
	createInvalid(t, c, &v1alpha1.LoadBalancer{ObjectMeta: metav1.ObjectMeta{Name: "no-listeners", Namespace: "default"}}, "spec.listeners")
}

func TestDNSZoneAndSecurityGroupValidation(t *testing.T) {
	c, _ := StartTestEnv(t)

	zone := &v1alpha1.DNSZone{
		ObjectMeta: metav1.ObjectMeta{Name: "internal", Namespace: "default"},
		Spec:       v1alpha1.DNSZoneSpec{Zone: "internal.example.test", Records: []v1alpha1.DNSRecord{{Name: "db", IPs: []string{"10.100.1.5"}}}},
	}
	createValid(t, c, zone)
	createInvalid(t, c, &v1alpha1.DNSZone{ObjectMeta: metav1.ObjectMeta{Name: "bad-zone", Namespace: "default"}, Spec: v1alpha1.DNSZoneSpec{Zone: "Not A Zone"}}, "spec.zone")
	createInvalid(t, c, &v1alpha1.DNSZone{ObjectMeta: metav1.ObjectMeta{Name: "bad-record", Namespace: "default"}, Spec: v1alpha1.DNSZoneSpec{Zone: "x.test", Records: []v1alpha1.DNSRecord{{Name: "db"}}}}, "spec.records[0].ips")

	group := &v1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: v1alpha1.SecurityGroupSpec{
			Ingress: []v1alpha1.SecurityGroupRule{
				{Protocol: v1alpha1.SecurityGroupProtocolTCP, Port: 8000, EndPort: 8100, CIDR: "0.0.0.0/0"},
				{SecurityGroupRef: "db"},
			},
			Egress: []v1alpha1.SecurityGroupRule{{CIDR: "0.0.0.0/0"}},
		},
	}
	createValid(t, c, group)

	invalid := map[string]v1alpha1.SecurityGroupRule{
		"exactly one of cidr and securityGroupRef must be set":  {Protocol: "tcp", Port: 80},
		"port requires the tcp or udp protocol":                 {Protocol: "icmp", Port: 80, CIDR: "10.0.0.0/8"},
		"endPort requires port and must not be lower than port": {Protocol: "tcp", Port: 90, EndPort: 80, CIDR: "10.0.0.0/8"},
		"cidr must be a CIDR":                                   {CIDR: "10.0.0.0"},
		"Unsupported value":                                     {Protocol: "sctp", CIDR: "10.0.0.0/8"},
	}
	for message, rule := range invalid {
		bad := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{GenerateName: "bad-", Namespace: "default"}, Spec: v1alpha1.SecurityGroupSpec{Ingress: []v1alpha1.SecurityGroupRule{rule}}}
		createInvalid(t, c, bad, message)
	}
}
