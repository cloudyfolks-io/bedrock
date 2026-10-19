package database

import (
	"context"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func goldenSubnet() v1alpha1.Subnet {
	return v1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "acme-web"},
		Spec:       v1alpha1.SubnetSpec{NetworkRef: "acme", CIDR: "10.100.0.0/24"},
	}
}

func goldenProxy() ProxyInput {
	return ProxyInput{
		Database: goldenDatabase(), Project: goldenProject(), Image: haproxy, Subnet: goldenSubnet(), Address: "10.100.0.2",
		VIP: "100.66.16.2", Port: 5432, RuntimeClass: "kata", DNSServers: []string{"100.66.0.53"}, Revision: 1,
	}
}

func TestProxyObjectsGolden(t *testing.T) {
	objects, err := ProxyObjects(goldenProxy())
	if err != nil {
		t.Fatal(err)
	}
	goldenList(t, "proxy_objects", objects)
}

func TestProxyDeploymentHasTheNetworkAnnotationsKataAndDNS(t *testing.T) {
	objects, _ := ProxyObjects(goldenProxy())
	deployment := objectOfKind(objects, "Deployment")
	annotations, _, _ := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "annotations")
	pinned := false
	for key, value := range annotations {
		pinned = pinned || (network.IsAddressAnnotation(key) && value == "10.100.0.2")
	}
	if !pinned {
		t.Fatalf("the pinned address is missing: %v", annotations)
	}
	attachment := network.PodAttachment(network.NIC{Namespace: "acme-web", Subnet: "default", Address: "10.100.0.2", SecurityGroups: []string{"database-0b0c0d0e"}})
	for key, value := range attachment.Annotations {
		if annotations[key] != value {
			t.Errorf("annotation %s = %q, want %q", key, annotations[key], value)
		}
	}
	runtimeClass, _, _ := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "runtimeClassName")
	policy, _, _ := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "dnsPolicy")
	servers, _, _ := unstructured.NestedStringSlice(deployment.Object, "spec", "template", "spec", "dnsConfig", "nameservers")
	strategy, _, _ := unstructured.NestedString(deployment.Object, "spec", "strategy", "type")
	replicas, _, _ := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	if runtimeClass != "kata" || policy != "None" || len(servers) != 1 || strategy != "Recreate" || replicas != 1 {
		t.Fatalf("pod spec: %q %q %v %q %d", runtimeClass, policy, servers, strategy, replicas)
	}
	if owners := deployment.GetOwnerReferences(); len(owners) != 1 || string(owners[0].UID) != gateUID || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("owners %v", owners)
	}
}

func TestProxyWithoutKataHasNoRuntimeClass(t *testing.T) {
	in := goldenProxy()
	in.RuntimeClass = ""
	objects, err := ProxyObjects(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedString(objectOfKind(objects, "Deployment").Object, "spec", "template", "spec", "runtimeClassName"); found {
		t.Fatal("a runtime class was set")
	}
}

func TestProxyClientCertificateNamesTheDatabaseUID(t *testing.T) {
	objects, _ := ProxyObjects(goldenProxy())
	spec := objectOfKind(objects, "Certificate").Object["spec"].(map[string]any)
	if spec["commonName"] != gateUID || spec["secretName"] != "orders-endpoint" {
		t.Fatalf("client certificate %v", spec)
	}
	if usages := spec["usages"].([]any); usages[len(usages)-1] != "client auth" {
		t.Fatalf("usages %v", usages)
	}
	if _, found := spec["dnsNames"]; found {
		t.Fatal("a client certificate has DNS names")
	}
}

func TestProxyObjectsRefuseWithoutDNSServers(t *testing.T) {
	in := goldenProxy()
	in.DNSServers = nil
	if _, err := ProxyObjects(in); err == nil {
		t.Fatal("a proxy without DNS servers was rendered")
	}
}

func TestSecurityGroupRules(t *testing.T) {
	db, project, subnet := goldenDatabase(), goldenProject(), goldenSubnet()
	rulesOf := func(db v1alpha1.Database, vip string) []string {
		var out []string
		for _, rule := range SecurityGroupObject(db, project, subnet, vip, 5432).Spec.Ingress {
			out = append(out, rule.Protocol+" "+rule.CIDR)
		}
		return out
	}
	if got := rulesOf(db, "100.66.16.2"); len(got) != 2 || got[0] != "tcp 10.100.0.0/24" || got[1] != "tcp 100.66.16.2/32" {
		t.Fatalf("no entry: %v", got)
	}
	db.Spec.AllowedCIDRs = []string{"192.168.5.0/28"}
	if got := rulesOf(db, "100.66.16.2"); len(got) != 2 || got[0] != "tcp 192.168.5.0/28" {
		t.Fatalf("one entry: %v", got)
	}
	db.Spec.AllowedCIDRs = []string{"192.168.5.0/28", "172.16.0.0/16"}
	if got := rulesOf(db, ""); len(got) != 2 || got[1] != "tcp 172.16.0.0/16" {
		t.Fatalf("a change: %v", got)
	}
	group := SecurityGroupObject(db, project, subnet, "100.66.16.2", 5432)
	if group.Name != "database-0b0c0d0e" || len(group.OwnerReferences) != 1 || group.Spec.Egress[0].Protocol != "all" || group.Spec.Egress[0].CIDR != "0.0.0.0/0" {
		t.Fatalf("group %+v", group)
	}
	for _, rule := range group.Spec.Ingress {
		if rule.Port != 5432 {
			t.Fatalf("rule %+v", rule)
		}
	}
}

func TestSecurityGroupFollowsAChangeOfTheAllowlistInTheCluster(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	db, project := storedDatabase(t, c, nil)
	subnet := goldenSubnet()
	must(t, ssa.Apply(ctx, c, SecurityGroupObject(*db, project, subnet, "100.66.16.2", 5432), v1alpha1.DatabaseFieldManager))
	db.Spec.AllowedCIDRs = []string{"192.168.5.0/28"}
	must(t, ssa.Apply(ctx, c, SecurityGroupObject(*db, project, subnet, "100.66.16.2", 5432), v1alpha1.DatabaseFieldManager))
	var group v1alpha1.SecurityGroup
	must(t, c.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: SecurityGroupName(db.UID)}, &group))
	if len(group.Spec.Ingress) != 2 || group.Spec.Ingress[0].CIDR != "192.168.5.0/28" {
		t.Fatalf("ingress %+v", group.Spec.Ingress)
	}
}

func addressWorld(t *testing.T) (client.Client, v1alpha1.Subnet) {
	t.Helper()
	c := startEnv(t)
	namespace := projectNamespace(t, c)
	subnet := goldenSubnet()
	subnet.Namespace = namespace
	return c, subnet
}

func databaseIn(t *testing.T, c client.Client, namespace string) *v1alpha1.Database {
	t.Helper()
	db := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "db-" + nextSequence(), Namespace: namespace}, Spec: newSpec()}
	create(t, c, db)
	must(t, c.Get(context.Background(), client.ObjectKeyFromObject(db), db))
	return db
}

func TestEnsureAddressGivesTwoConcurrentCreatesTwoAddresses(t *testing.T) {
	c, subnet := addressWorld(t)
	ctx := context.Background()
	a := &Allocator{}
	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := range results {
		db := databaseIn(t, c, subnet.Namespace)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			address, err := a.EnsureAddress(ctx, c, c, db, subnet, 5432)
			if err != nil {
				t.Error(err)
			}
			results[i] = address
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, address := range results {
		if address == "" || seen[address] {
			t.Fatalf("addresses %v", results)
		}
		seen[address] = true
	}
}

func TestEnsureAddressKeepsTheAddressAcrossARestartAndWithLostStatus(t *testing.T) {
	c, subnet := addressWorld(t)
	ctx := context.Background()
	db := databaseIn(t, c, subnet.Namespace)
	address, err := (&Allocator{}).EnsureAddress(ctx, c, c, db, subnet, 5432)
	must(t, err)
	if address != "10.100.0.2" {
		t.Fatalf("first address %s", address)
	}
	var stored v1alpha1.Database
	must(t, c.Get(ctx, client.ObjectKeyFromObject(db), &stored))
	if len(stored.Status.Addresses) != 1 || stored.Status.Addresses[0].IP != address || stored.Status.Addresses[0].Subnet != "default" || stored.Status.Addresses[0].Port != 5432 {
		t.Fatalf("status %+v", stored.Status.Addresses)
	}
	again, err := (&Allocator{}).EnsureAddress(ctx, c, c, &stored, subnet, 5432)
	must(t, err)
	if again != address {
		t.Fatalf("a restart moved the address to %s", again)
	}

	proxy := goldenProxy()
	proxy.Database, proxy.Address = stored, address
	proxy.Database.Namespace = subnet.Namespace
	proxy.Subnet = subnet
	objects, err := ProxyObjects(proxy)
	must(t, err)
	must(t, ssa.Apply(ctx, c, objectOfKind(objects, "Deployment"), v1alpha1.DatabaseFieldManager))
	stored.Status.Addresses = nil
	must(t, c.Status().Update(ctx, &stored))
	lost, err := (&Allocator{}).EnsureAddress(ctx, c, c, &stored, subnet, 5432)
	must(t, err)
	if lost != address {
		t.Fatalf("lost status moved the address to %s", lost)
	}
}

func TestEnsureAddressAvoidsPinsOfOtherDatabasesAndOtherWorkloads(t *testing.T) {
	c, subnet := addressWorld(t)
	ctx := context.Background()
	one := databaseIn(t, c, subnet.Namespace)
	a := &Allocator{}
	first, err := a.EnsureAddress(ctx, c, c, one, subnet, 5432)
	must(t, err)
	create(t, c, &v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Name: "pinned", Namespace: subnet.Namespace}, Spec: v1alpha1.ContainerSpec{Image: "x", Network: v1alpha1.ContainerNetwork{Address: "10.100.0.3"}}})
	two := databaseIn(t, c, subnet.Namespace)
	second, err := a.EnsureAddress(ctx, c, c, two, subnet, 5432)
	must(t, err)
	if first != "10.100.0.2" || second != "10.100.0.4" {
		t.Fatalf("addresses %s %s", first, second)
	}
}

func TestEnsureAddressOfAFullSubnetFails(t *testing.T) {
	c, subnet := addressWorld(t)
	subnet.Spec.CIDR = "10.100.0.0/30"
	db := databaseIn(t, c, subnet.Namespace)
	if _, err := (&Allocator{}).EnsureAddress(context.Background(), c, c, db, subnet, 5432); err != nil {
		t.Fatal(err)
	}
	other := databaseIn(t, c, subnet.Namespace)
	if _, err := (&Allocator{}).EnsureAddress(context.Background(), c, c, other, subnet, 5432); err != network.ErrNoFreeAddress {
		t.Fatalf("err %v", err)
	}
	_ = corev1.Pod{}
}
