package database

import (
	"context"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func frontendFixture() (*flow, *corev1.ConfigMap, *v1alpha1.Network, *unstructured.Unstructured, *unstructured.Unstructured) {
	db := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "acme-web", UID: "uid-db"}}
	db.Spec.Endpoint = &v1alpha1.DatabaseEndpointSpec{Address: "100.127.2.10", LoadBalancerRef: "source-frontend", Port: 6432}
	project := Project{Namespace: db.Namespace, Tenant: "acme", Name: "web"}
	subnet := v1alpha1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: db.Namespace, UID: "uid-subnet"}}
	subnet.Spec.CIDR = "10.50.0.0/24"
	subnet.Spec.NetworkRef = "net-acme"
	parent := &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "net-acme", UID: "uid-network"}}
	parent.Spec.TenantRef = "acme"
	parent.Status.VPC = "net-acme"
	parent.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue}}
	claim := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.DatabaseEndpointClaimName(db.Spec.Endpoint.LoadBalancerRef), Namespace: PlatformNamespace, UID: "uid-claim"},
		Immutable:  ptr.To(true),
		Data: map[string]string{
			"uid": string(db.UID), "namespace": db.Namespace, "database": db.Name,
			"address": db.Spec.Endpoint.Address, "port": "6432",
			"loadBalancerID": "source-id", "projectID": "project-id", "environment": "staging",
			"legacyVPC": "old-router", "legacyNamespace": "old-web",
			"subnetUID": string(subnet.UID), "network": subnet.Spec.NetworkRef,
			"jobCIDR": "10.99.0.0/24",
		},
	}
	plane := fabric.PlaneSubnet(fabric.Meta{
		Name: network.LBSubnetName(parent.Status.VPC), Annotations: network.OwnerAnnotations(parent),
	}, parent.Status.VPC, "100.127.0.0/16", "100.127.0.1")
	plane.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	backend := fabric.PlaneSubnet(fabric.Meta{
		Name: network.FabricName(db.Namespace, subnet.Name), Annotations: network.OwnerAnnotations(&subnet),
	}, parent.Status.VPC, subnet.Spec.CIDR, "10.50.0.1")
	backend.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	return &flow{db: db, project: project, subnet: subnet, port: 5432}, claim, parent, plane, backend
}

func endpointApproval(claim *corev1.ConfigMap, name string) *corev1.ConfigMap {
	marker := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PlatformNamespace},
		Immutable:  ptr.To(true),
		Data:       map[string]string{"claimUID": string(claim.UID)},
	}
	for _, key := range []string{"uid", "namespace", "database", "subnetUID", "network"} {
		marker.Data[key] = claim.Data[key]
	}
	return marker
}

func TestLegacyEndpointClaimBindsDatabaseSubnetAndFrontend(t *testing.T) {
	in, claim, _, _, _ := frontendFixture()
	if rejected := validateEndpointClaim(*in.db, in.subnet, *claim); rejected != nil {
		t.Fatal(rejected)
	}
	for key, value := range map[string]string{
		"uid": "recreated-db", "namespace": "somewhere-else", "database": "payments",
		"address": "100.127.2.11", "port": "5432", "projectID": "",
		"subnetUID": "replaced-subnet", "network": "other-network", "jobCIDR": "fd23::/64",
	} {
		t.Run(key, func(t *testing.T) {
			modified := claim.DeepCopy()
			modified.Data[key] = value
			if rejected := validateEndpointClaim(*in.db, in.subnet, *modified); rejected == nil {
				t.Fatal("accepted an invalid endpoint claim")
			}
		})
	}
	if PinnedAddress(*in.db) != "" {
		t.Fatal("the legacy VIP must not be assigned to the proxy")
	}
	host, port := customerEndpoint(*in.db, "10.50.0.2", int32(in.port))
	if host != in.db.Spec.Endpoint.Address || port != in.db.Spec.Endpoint.Port {
		t.Fatalf("wrong customer endpoint %s:%d", host, port)
	}
}

func TestLegacyEndpointKeepsTheMigrationCIDRUntilTheApprovedOpen(t *testing.T) {
	in, claim, parent, plane, backend := frontendFixture()
	in.db.Spec.AllowedCIDRs = []string{"172.20.0.0/16"}
	claim.Data["jobCIDR"] = "10.99.0.15/24"
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(claim, parent, plane, backend).Build()
	r := &Reconciler{Client: c, Live: c}
	ctx := context.Background()
	ref := in.db.Spec.Endpoint.LoadBalancerRef
	if rejected, err := r.prepareLegacyEndpoint(ctx, in); err != nil || rejected == nil || rejected.Reason != ReasonProxyWaiting {
		t.Fatalf("accepted an unsuspended database without start approval: %v %v", rejected, err)
	}
	start := endpointApproval(claim, v1alpha1.DatabaseEndpointStartName(ref))
	must(t, c.Create(ctx, start))
	check := func(want string) {
		t.Helper()
		if rejected, err := r.prepareLegacyEndpoint(ctx, in); err != nil || rejected != nil {
			t.Fatalf("could not prepare the legacy endpoint: %v %v", rejected, err)
		}
		db := proxyDatabase(in)
		if !slices.Equal(db.Spec.AllowedCIDRs, []string{want}) || !slices.Equal(in.db.Spec.AllowedCIDRs, []string{"172.20.0.0/16"}) {
			t.Fatalf("want effective CIDR %s and unmodified spec, got %v and %v", want, db.Spec.AllowedCIDRs, in.db.Spec.AllowedCIDRs)
		}
	}
	check("10.99.0.0/24")
	marker := endpointApproval(claim, v1alpha1.DatabaseEndpointOpenName(ref))
	marker.Data["claimUID"] = "old-claim"
	must(t, c.Create(ctx, marker))
	check("10.99.0.0/24")
	must(t, c.Delete(ctx, marker))
	marker.SetResourceVersion("")
	marker.Data["claimUID"] = string(claim.UID)
	must(t, c.Create(ctx, marker))
	check("172.20.0.0/16")
}

func TestLegacyEndpointAdoptsOnlyTheApprovedFrontendAfterTheProxyIsReady(t *testing.T) {
	in, claim, parent, plane, backend := frontendFixture()
	ctx := context.Background()
	address := "10.50.0.12"
	proxy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: EndpointName(*in.db), Namespace: in.db.Namespace, UID: "uid-proxy",
			OwnerReferences: []metav1.OwnerReference{ownerRefOf(*in.db)},
		},
		Spec:   appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "proxy-set", Namespace: in.db.Namespace, UID: "uid-proxy-set",
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(proxy, appsv1.SchemeGroupVersion.WithKind("Deployment"))},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "proxy-pod", Namespace: in.db.Namespace,
			Labels:          map[string]string{managedProxyLabel: string(in.db.UID), network.NetworkClassLabel: network.NetworkClassManaged},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	fabricSubnet := network.FabricName(in.db.Namespace, in.subnet.Name)
	ip := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"namespace": in.db.Namespace, "podName": pod.Name, "subnet": fabricSubnet, "v4IpAddress": address},
	}}
	ip.SetName("proxy-ip")
	ip.SetGroupVersionKind(fabric.IPGVK)
	ip.SetLabels(map[string]string{"fabric.cloudyfolks.io/subnet": fabricSubnet})
	old := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"frontend": map[string]any{"vip": in.db.Spec.Endpoint.Address},
			"vpc":      claim.Data["legacyVPC"], "namespace": claim.Data["legacyNamespace"],
			"selector": []any{"old:backend"},
			"ports":    []any{map[string]any{"port": int64(in.db.Spec.Endpoint.Port), "protocol": "TCP"}},
		},
	}}
	old.SetName(in.db.Spec.Endpoint.LoadBalancerRef)
	old.SetGroupVersionKind(fabric.LoadBalancerGVK)
	old.SetLabels(map[string]string{
		LabelManagedBy: legacyLBManager, LabelInstance: claim.Data["loadBalancerID"],
		sourceProjectLabel: claim.Data["projectID"], sourceEnvironmentLabel: claim.Data["environment"],
	})
	start := endpointApproval(claim, v1alpha1.DatabaseEndpointStartName(in.db.Spec.Endpoint.LoadBalancerRef))
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(claim, start, parent, plane, backend, old, proxy, rs, pod, ip).Build()
	r := &Reconciler{Client: c, Live: c}
	if rejected, err := r.prepareLegacyEndpoint(ctx, in); err != nil || rejected != nil {
		t.Fatalf("could not prepare the endpoint: %v %v", rejected, err)
	}
	in.db.Spec.Suspended = true
	if rejected, err := r.applyLegacyFrontend(ctx, in, address); err != nil || rejected != nil {
		t.Fatalf("unexpected error while suspended: %v %v", rejected, err)
	}
	fresh, err := endpointObject(ctx, c, fabric.LoadBalancerGVK, old.GetName())
	must(t, err)
	if !in.legacy.isLegacyFrontend(fresh) {
		t.Fatal("the suspended database changed the source frontend")
	}
	in.db.Spec.Suspended = false

	foreign := *in.legacy
	foreign.projectID = "foreign-project"
	in.legacy = &foreign
	if rejected, err := r.applyLegacyFrontend(ctx, in, address); err != nil || rejected == nil {
		t.Fatalf("expected a refusal for a foreign frontend, got %v %v", rejected, err)
	}
	reservation := in.legacy.vip(*in.db, in.project)
	observed, err := endpointObject(ctx, c, fabric.VipGVK, reservation.GetName())
	if err != nil || observed != nil {
		t.Fatalf("expected no reservation before ownership validation, got %v %v", observed, err)
	}

	in.legacy.projectID = claim.Data["projectID"]
	duplicate := fabric.SwitchLBRule(fabric.Meta{Name: "other-frontend", Annotations: map[string]string{frontendVPCAnnotation: in.legacy.vpc}},
		in.db.Spec.Endpoint.Address, in.db.Namespace, []string{address}, nil)
	must(t, c.Create(ctx, duplicate))
	if rejected, err := r.applyLegacyFrontend(ctx, in, address); err != nil || rejected == nil {
		t.Fatalf("expected refusal for another frontend with the same VIP and VPC, got %v %v", rejected, err)
	}
	must(t, c.Delete(ctx, duplicate))

	if rejected, err := r.applyLegacyFrontend(ctx, in, address); err != nil || rejected != nil {
		t.Fatalf("expected the first pass to reserve the VIP, got %v %v", rejected, err)
	}
	reservation, err = endpointObject(ctx, c, fabric.VipGVK, reservation.GetName())
	must(t, err)
	if reservation == nil {
		t.Fatal("missing reservation")
	}
	next, _, _ := unstructured.NestedString(reservation.Object, "spec", "v4ip")
	if next != in.db.Spec.Endpoint.Address {
		t.Fatalf("reserved %s instead of %s", next, in.db.Spec.Endpoint.Address)
	}
	reservation.Object["status"] = map[string]any{"v4ip": next, "mac": "00:00:5e:00:53:01"}
	must(t, c.Update(ctx, reservation))
	if rejected, err := r.applyLegacyFrontend(ctx, in, address); err != nil || rejected != nil {
		t.Fatalf("could not adopt the endpoint: %v %v", rejected, err)
	}
	fresh, err = endpointObject(ctx, c, fabric.LoadBalancerGVK, old.GetName())
	must(t, err)
	want := in.legacy.loadBalancer(*in.db, in.project, address, in.port)
	if !ownedFrontend(fresh, *in.db) || !frontendSpecMatches(fresh, want) {
		t.Fatal("the frontend was not retargeted to the separate customer proxy NIC")
	}
	in.db.Status.ProxyAddress = address
	in.db.Status.Addresses = []v1alpha1.DatabaseAddress{{Subnet: in.subnet.Name, IP: next, Port: in.db.Spec.Endpoint.Port}}
	ready, err := r.legacyFrontendReady(ctx, in)
	if err != nil || ready {
		t.Fatalf("reported readiness without the Fabric child or endpoints: %t %v", ready, err)
	}
	childName := frontendChildPrefix + fresh.GetName()
	serviceName := frontendServicePrefix + childName
	service := in.db.Namespace + "/" + serviceName
	fresh.Object["status"] = map[string]any{"vip": next, "ports": "6432/TCP", "service": service}
	must(t, c.Update(ctx, fresh))
	child := fabric.SwitchLBRule(fabric.Meta{
		Name: childName, Labels: map[string]string{frontendOwnerLabel: fresh.GetName()},
		Annotations: map[string]string{frontendVPCAnnotation: in.legacy.vpc, fabric.AnnotationServiceHealthCheck: "false"},
	}, next, in.db.Namespace, []string{address}, []fabric.RouterLBPort{{Name: "database", Port: 6432, TargetPort: 5432, Protocol: "TCP"}})
	child.Object["status"] = map[string]any{"service": service}
	must(t, c.Create(ctx, child))
	targetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: in.db.Namespace, Annotations: map[string]string{
			frontendVIPAnnotation: next, frontendVPCAnnotation: in.legacy.vpc, fabric.AnnotationServiceHealthCheck: "false",
		}},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Protocol: corev1.ProtocolTCP, Port: 6432, TargetPort: intstr.FromInt32(5432)}}},
	}
	must(t, c.Create(ctx, targetService))
	endpoints := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: in.db.Namespace},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: address}},
			Ports:     []corev1.EndpointPort{{Port: 5432, Protocol: corev1.ProtocolTCP}},
		}},
	}
	must(t, c.Create(ctx, endpoints))
	if ready, err := r.legacyFrontendReady(ctx, in); err != nil || !ready {
		t.Fatalf("the complete frontend is not ready: %t %v", ready, err)
	}
	if gone, err := r.removeLegacyFrontend(ctx, *in.db); err != nil || gone {
		t.Fatalf("the frontend reports withdrawal before the child is gone: %t %v", gone, err)
	}
	if gone, err := r.removeLegacyFrontend(ctx, *in.db); err != nil || gone {
		t.Fatalf("the frontend reports withdrawal while the child exists: %t %v", gone, err)
	}
	must(t, c.Delete(ctx, child))
	if gone, err := r.removeLegacyFrontend(ctx, *in.db); err != nil || gone {
		t.Fatalf("the frontend reports withdrawal while the Service exists: %t %v", gone, err)
	}
	must(t, c.Delete(ctx, targetService))
	if gone, err := r.removeLegacyFrontend(ctx, *in.db); err != nil || !gone {
		t.Fatalf("the frontend does not report withdrawal: %t %v", gone, err)
	}
}

func TestLegacyProxyUsesLiveFabricIPRecords(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	in, _, _, _, _ := frontendFixture()
	in.db.Namespace = "legacy-proxy-live-ip"
	in.subnet.Namespace = in.db.Namespace
	must(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: in.db.Namespace}}))
	labels := map[string]string{managedProxyLabel: string(in.db.UID), network.NetworkClassLabel: network.NetworkClassManaged}
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: haproxy}}}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: podSpec}
	selector := &metav1.LabelSelector{MatchLabels: labels}
	proxy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: EndpointName(*in.db), Namespace: in.db.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRefOf(*in.db)},
		},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1), Template: template, Selector: selector},
	}
	must(t, c.Create(ctx, proxy))
	proxy.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1}
	must(t, c.Status().Update(ctx, proxy))
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "proxy-set", Namespace: in.db.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(proxy, appsv1.SchemeGroupVersion.WithKind("Deployment"))},
		},
		Spec: appsv1.ReplicaSetSpec{Replicas: ptr.To[int32](1), Template: template, Selector: selector},
	}
	must(t, c.Create(ctx, replicaSet))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "proxy-pod", Namespace: in.db.Namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(replicaSet, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))},
		},
		Spec: podSpec,
	}
	must(t, c.Create(ctx, pod))
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	must(t, c.Status().Update(ctx, pod))
	fabricSubnet := network.FabricName(in.db.Namespace, in.subnet.Name)
	const address = "10.50.0.2"
	ipRecord := func(name, subnet, ipv4 string) *unstructured.Unstructured {
		ip := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"namespace": in.db.Namespace, "podName": pod.Name, "subnet": subnet, "v4IpAddress": ipv4},
		}}
		ip.SetGroupVersionKind(fabric.IPGVK)
		ip.SetName(name)
		ip.SetLabels(map[string]string{"fabric.cloudyfolks.io/subnet": subnet})
		return ip
	}
	r := &Reconciler{Client: c, Live: c}
	assertReady := func(want bool) {
		t.Helper()
		got, err := r.readyLegacyProxy(ctx, *in.db, in.subnet, address)
		if err != nil || got != want {
			t.Fatalf("proxy readiness = %t, want %t: %v", got, want, err)
		}
	}
	must(t, c.Create(ctx, ipRecord("different-address", fabricSubnet, "10.50.0.20")))
	must(t, c.Create(ctx, ipRecord("different-subnet", "another-subnet", address)))
	assertReady(false)
	correct := ipRecord("proxy-address", fabricSubnet, address)
	must(t, c.Create(ctx, correct))
	assertReady(true)
	duplicate := ipRecord("duplicate-address", fabricSubnet, address)
	must(t, unstructured.SetNestedField(duplicate.Object, "foreign-namespace", "spec", "namespace"))
	must(t, c.Create(ctx, duplicate))
	assertReady(false)
	must(t, c.Delete(ctx, correct))
	assertReady(false)
}

func TestLegacyEndpointRefusesAVIPOutsideTheApprovedPlaneWithoutCreatingIt(t *testing.T) {
	in, claim, parent, plane, backend := frontendFixture()
	must(t, unstructured.SetNestedField(plane.Object, "100.128.0.0/16", "spec", "cidrBlock"))
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(claim, parent, plane, backend).Build()
	rejected, err := (&Reconciler{Client: c, Live: c}).prepareLegacyEndpoint(context.Background(), in)
	if err != nil || rejected == nil || rejected.Reason != ReasonEndpointAddressInvalid {
		t.Fatalf("expected an invalid VIP refusal, got %v %v", rejected, err)
	}
}

func TestLegacyEndpointRecognizesAStandaloneRuleAndRefusesTwoOwners(t *testing.T) {
	in, claim, parent, plane, backend := frontendFixture()
	in.db.Spec.Suspended = true
	spec := in.db.Spec.Endpoint
	old := fabric.SwitchLBRule(fabric.Meta{
		Name: spec.LoadBalancerRef,
		Labels: map[string]string{
			LabelManagedBy: legacyLBManager, LabelInstance: claim.Data["loadBalancerID"],
			sourceProjectLabel: claim.Data["projectID"], sourceEnvironmentLabel: claim.Data["environment"],
		},
		Annotations: map[string]string{frontendVPCAnnotation: claim.Data["legacyVPC"], frontendSubnetAnnotation: "source-subnet"},
	}, spec.Address, claim.Data["legacyNamespace"], nil, []fabric.RouterLBPort{{Name: "database", Port: spec.Port, TargetPort: spec.Port, Protocol: "TCP"}})
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(claim, parent, plane, backend, old).Build()
	r := &Reconciler{Client: c, Live: c}
	ctx := context.Background()
	if rejected, err := r.prepareLegacyEndpoint(ctx, in); err != nil || rejected != nil {
		t.Fatalf("could not prepare the endpoint: %v %v", rejected, err)
	}
	have, rejected, err := in.legacy.currentFrontend(ctx, c)
	if err != nil || rejected != nil || have == nil || !in.legacy.isLegacyFrontend(have) {
		t.Fatalf("could not verify the legacy rule: %v %v %v", have, rejected, err)
	}
	if conflict, err := in.legacy.checkFrontendConflict(ctx, c, have); err != nil || conflict != nil {
		t.Fatalf("the approved standalone rule has no conflict: %v %v", conflict, err)
	}
	approved := in.legacy.switchRule(*in.db, in.project, in.subnet, "10.50.0.12", in.port)
	if approved.GetAnnotations()[frontendSubnetAnnotation] != network.FabricName(in.db.Namespace, in.subnet.Name) ||
		!ownedFrontend(approved, *in.db) {
		t.Fatal("the new standalone rule must identify the target subnet and database")
	}
	unified := in.legacy.loadBalancer(*in.db, in.project, "10.50.0.12", in.port)
	must(t, c.Create(ctx, unified))
	if _, conflict, err := in.legacy.currentFrontend(ctx, c); err != nil || conflict == nil {
		t.Fatalf("expected refusal with two existing Fabric frontend kinds, got %v %v", conflict, err)
	}
}
