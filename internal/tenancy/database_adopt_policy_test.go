package tenancy

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const databaseAdoptMessage = "only an administrator may set the adopt annotation of a database user or schema"

func adoptingDatabaseUser(name string, annotations map[string]string) *v1alpha1.DatabaseUser {
	return &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme", Annotations: annotations},
		Spec:       v1alpha1.DatabaseUserSpec{DatabaseRef: "orders"},
	}
}

func adoptingSchema(name string, annotations map[string]string) *v1alpha1.DatabaseSchema {
	return &v1alpha1.DatabaseSchema{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "t-acme", Annotations: annotations},
		Spec:       v1alpha1.DatabaseSchemaSpec{DatabaseRef: "orders"},
	}
}

func databaseAdoptWorld(t *testing.T) (admin, alice, root client.Client) {
	t.Helper()
	admin, cfg := startEnv(t)
	mustCreate(t, admin,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "database-access-writer"},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"databaseusers", "databaseschemas", "securitygroups"}, Verbs: []string{"create", "update", "get", "delete"}},
				{APIGroups: []string{""}, Resources: []string{"pods", "configmaps"}, Verbs: []string{"create", "update", "get", "delete"}},
				{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"create"}},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "database-access-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "database-access-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"},
				{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"},
			},
		},
		namespaceNamed("t-acme", map[string]string{v1alpha1.LabelTenant: "acme"}),
	)
	applyFile(t, admin, manifestPath("86-tenancy", "90-database-adopt-policy.yaml"))
	alice = actingAs(t, cfg, "bedrock:alice")
	root = actingAs(t, cfg, "bedrock:root", "bedrock:bedrock-admins", "system:authenticated")
	probe := adoptingDatabaseUser("probe", map[string]string{v1alpha1.AnnotationAdopt: "true"})
	if err := eventuallyForbidden(t, denyCreate(admin, alice, probe)); err == nil {
		t.Fatal("the database adopt policy never denied")
	}
	return admin, alice, root
}

func requireAdoptDenied(t *testing.T, err error, what string) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), databaseAdoptMessage) {
		t.Fatalf("%s: err = %v", what, err)
	}
}

func TestNonAdminCannotAdoptADatabaseUserOrSchema(t *testing.T) {
	_, alice, _ := databaseAdoptWorld(t)
	ctx := context.Background()
	annotated := map[string]string{v1alpha1.AnnotationAdopt: "true"}
	requireAdoptDenied(t, alice.Create(ctx, adoptingDatabaseUser("u-create", annotated)), "user create")
	requireAdoptDenied(t, alice.Create(ctx, adoptingSchema("s-create", annotated)), "schema create")
	user, schema := adoptingDatabaseUser("u-plain", nil), adoptingSchema("s-plain", nil)
	mustCreate(t, alice, user, schema)
	user.Annotations, schema.Annotations = annotated, annotated
	requireAdoptDenied(t, alice.Update(ctx, user), "user update")
	requireAdoptDenied(t, alice.Update(ctx, schema), "schema update")
}

func TestNonAdminCannotRemoveTheAdoptAnnotation(t *testing.T) {
	admin, alice, _ := databaseAdoptWorld(t)
	adopted := adoptingDatabaseUser("u-adopted", map[string]string{v1alpha1.AnnotationAdopt: "true"})
	mustCreate(t, admin, adopted)
	adopted.Annotations = nil
	requireAdoptDenied(t, alice.Update(context.Background(), adopted), "user update")
}

func TestAdminCanAdoptAndNonAdminKeepsEditing(t *testing.T) {
	_, alice, root := databaseAdoptWorld(t)
	annotated := map[string]string{v1alpha1.AnnotationAdopt: "true"}
	user := adoptingDatabaseUser("u-root", annotated)
	mustCreate(t, root, user, adoptingSchema("s-root", annotated))
	user.Spec.Grants = []v1alpha1.DatabaseGrant{{Database: "app", Level: "readonly"}}
	if err := alice.Update(context.Background(), user); err != nil {
		t.Fatalf("an unrelated change must pass: %v", err)
	}
}

func TestNonAdminCannotChangeTheDatabaseSecurityGroup(t *testing.T) {
	admin, alice, root := databaseAdoptWorld(t)
	ctx := context.Background()
	group := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{
		Name: "database-100", Namespace: "t-acme",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: v1alpha1.GroupVersion.String(), Kind: "Database", Name: "orders", UID: "uid-db", Controller: ptr.To(true),
		}},
	}}
	mustCreate(t, admin, group)
	group.Spec.Ingress = []v1alpha1.SecurityGroupRule{{CIDR: "0.0.0.0/0", Port: 5432, Protocol: "tcp"}}
	if err := alice.Update(ctx, group); !apierrors.IsForbidden(err) {
		t.Fatalf("the tenant could change database ingress: %v", err)
	}
	if err := alice.Delete(ctx, group); !apierrors.IsForbidden(err) {
		t.Fatalf("the tenant could delete the database security group: %v", err)
	}
	if err := root.Update(ctx, group); err != nil {
		t.Fatalf("the admin could not change the database security group: %v", err)
	}
	ordinary := &v1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Name: "customer-rules", Namespace: "t-acme"}}
	mustCreate(t, alice, ordinary)
	if err := alice.Delete(ctx, ordinary); err != nil {
		t.Fatalf("the tenant could not delete an independent security group: %v", err)
	}
}

func TestNonAdminCannotUseTheManagedDatabaseProxyIdentity(t *testing.T) {
	admin, alice, _ := databaseAdoptWorld(t)
	ctx := context.Background()
	const label = "bedrock.cloudyfolks.io/database-gate-client"
	const identity = "approved-database"
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "test:1"}}}
	forged := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: "t-acme", Labels: map[string]string{label: identity}},
		Spec:       podSpec,
	}
	if err := eventuallyForbidden(t, denyCreate(admin, alice, forged)); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database proxy resource or identity") {
		t.Fatalf("the tenant could claim the gate identity: %v", err)
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "customer-app", Namespace: "t-acme"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "customer"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "customer", label: identity}}, Spec: podSpec},
		},
	}
	if err := alice.Create(ctx, deployment); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database proxy identity on a pod template") {
		t.Fatalf("the tenant could copy the gate identity to a workload template: %v", err)
	}
	settings := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "bedrock-db-proxy-test", Namespace: "t-acme"}}
	mustCreate(t, admin, settings)
	if err := alice.Delete(ctx, settings); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "database proxy resource or identity") {
		t.Fatalf("the tenant could replace proxy configuration: %v", err)
	}
	delete(deployment.Spec.Template.Labels, label)
	mustCreate(t, alice, deployment)
}

func TestDatabaseProxyControllerUpdates(t *testing.T) {
	admin, cfg := startEnv(t)
	ctx := context.Background()
	const (
		aaq           = "system:serviceaccount:aaq:aaq-controller"
		fabric        = "system:serviceaccount:kube-system:ovn"
		fabricDaemon  = "system:serviceaccount:kube-system:ovn-ovs"
		unrelatedAAQ  = "system:serviceaccount:tenant-tools:aaq-controller"
		tenant        = "bedrock:alice"
		gate          = "ApplicationAwareQuotaGate"
		gateIdentity  = "bedrock.cloudyfolks.io/database-gate-client"
		databaseUID   = "40e5e9a9-4e91-4e28-9c39-a743f2cf5bb1"
		proxyPrefix   = "bedrock-db-proxy-" + databaseUID + "-"
		replicaSet    = proxyPrefix + "9c9d6699cd"
		primary       = "fabric.cloudyfolks.io/"
		secondary     = "vpc.kube-system.fabric.cloudyfolks.io/"
		policyMessage = "database proxy resource or identity"
	)
	subjects := []rbacv1.Subject{}
	for _, actor := range []string{aaq, fabric, fabricDaemon, unrelatedAAQ, tenant} {
		subjects = append(subjects, rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: actor})
	}
	mustCreate(t, admin,
		namespaceNamed("t-acme", map[string]string{v1alpha1.LabelTenant: "acme"}),
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "proxy-pod-writer", Namespace: "t-acme"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create", "update", "patch", "delete"}}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "proxy-pod-writer", Namespace: "t-acme"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "proxy-pod-writer"},
			Subjects:   subjects,
		},
	)
	applyFile(t, admin, manifestPath("86-tenancy", "90-database-adopt-policy.yaml"))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: replicaSet + "-", Namespace: "t-acme",
			Labels: map[string]string{
				gateIdentity: databaseUID, "network.cloudyfolks.com/class": "managed",
				"app.kubernetes.io/managed-by": "bedrock", "app.kubernetes.io/component": "endpoint",
				"app.kubernetes.io/instance": "db-" + databaseUID,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet, UID: "replicaset", Controller: ptr.To(true),
			}},
			Annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": "kube-system/vpc",
				primary + "logical_switch":    "tenant-managed",
				primary + "port_security":     "true",
				secondary + "logical_switch":  "t-acme-vpc",
				secondary + "ip_address":      "192.0.2.8",
			},
		},
		Spec: corev1.PodSpec{
			SchedulingGates: []corev1.PodSchedulingGate{{Name: gate}},
			Containers:      []corev1.Container{{Name: "proxy", Image: "test:1"}},
		},
	}
	probe := pod.DeepCopy()
	probe.Name = proxyPrefix + "probe"
	tenantClient := actingAs(t, cfg, tenant)
	err := eventuallyForbidden(t, func() error { return tenantClient.Create(ctx, probe, client.DryRunAll) })
	if !strings.Contains(err.Error(), policyMessage) {
		t.Fatalf("proxy policy probe: %v", err)
	}
	mustCreate(t, admin, pod)
	if !strings.HasPrefix(pod.Name, proxyPrefix) || strings.HasPrefix(pod.Name, replicaSet) {
		t.Fatalf("expected the API server to truncate the generated proxy name: %s", pod.Name)
	}
	clearGate := func(pod *corev1.Pod) { pod.Spec.SchedulingGates = nil }
	allocate := func(pod *corev1.Pod) {
		pod.Annotations[primary+"ip_address"] = "198.51.100.2"
		for _, prefix := range []string{primary, secondary} {
			pod.Annotations[prefix+"allocated"] = "true"
			pod.Annotations[prefix+"cidr"] = "198.51.100.0/24"
			pod.Annotations[prefix+"gateway"] = "198.51.100.1"
			pod.Annotations[prefix+"logical_router"] = "router"
			pod.Annotations[prefix+"mac_address"] = "02:00:00:00:00:01"
			pod.Annotations[prefix+"pod_nic_type"] = "veth-pair"
		}
	}
	for _, change := range []struct {
		name  string
		actor string
		apply func(*corev1.Pod)
	}{
		{name: "tenant clears quota gate", actor: tenant, apply: clearGate},
		{name: "unrelated quota controller clears gate", actor: unrelatedAAQ, apply: clearGate},
		{name: "quota controller changes an annotation", actor: aaq, apply: func(pod *corev1.Pod) {
			clearGate(pod)
			pod.Annotations[primary+"allocated"] = "true"
		}},
		{name: "quota controller changes the image", actor: aaq, apply: func(pod *corev1.Pod) {
			clearGate(pod)
			pod.Spec.Containers[0].Image = "test:2"
		}},
		{name: "quota controller changes the node selector", actor: aaq, apply: func(pod *corev1.Pod) {
			clearGate(pod)
			pod.Spec.NodeSelector = map[string]string{"region": "elsewhere"}
		}},
		{name: "quota controller changes the proxy identity", actor: aaq, apply: func(pod *corev1.Pod) {
			clearGate(pod)
			pod.Labels[gateIdentity] = "elsewhere"
		}},
		{name: "quota controller changes the generated name", actor: aaq, apply: func(pod *corev1.Pod) {
			clearGate(pod)
			pod.GenerateName = "elsewhere-"
		}},
		{name: "Fabric daemon sets allocation", actor: fabricDaemon, apply: allocate},
		{name: "Fabric controller clears quota gate", actor: fabric, apply: clearGate},
		{name: "Fabric controller changes the static address", actor: fabric, apply: func(pod *corev1.Pod) {
			allocate(pod)
			pod.Annotations[secondary+"ip_address"] = "192.0.2.9"
		}},
		{name: "Fabric controller changes the primary switch", actor: fabric, apply: func(pod *corev1.Pod) {
			allocate(pod)
			pod.Annotations[primary+"logical_switch"] = "elsewhere"
		}},
		{name: "Fabric controller removes port security", actor: fabric, apply: func(pod *corev1.Pod) {
			allocate(pod)
			delete(pod.Annotations, primary+"port_security")
		}},
		{name: "Fabric controller changes the owner", actor: fabric, apply: func(pod *corev1.Pod) {
			allocate(pod)
			pod.OwnerReferences[0].UID = "elsewhere"
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			updated := pod.DeepCopy()
			change.apply(updated)
			err := actingAs(t, cfg, change.actor).Update(ctx, updated, client.DryRunAll)
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), policyMessage) {
				t.Fatalf("change must be denied by the proxy policy: %v", err)
			}
		})
	}
	aaqClient := actingAs(t, cfg, aaq)
	fabricClient := actingAs(t, cfg, fabric)
	for _, actor := range []client.Client{aaqClient, fabricClient} {
		if err := actor.Create(ctx, probe.DeepCopy(), client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), policyMessage) {
			t.Fatalf("controller proxy creation must be denied: %v", err)
		}
		if err := actor.Delete(ctx, pod.DeepCopy(), client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), policyMessage) {
			t.Fatalf("controller proxy deletion must be denied: %v", err)
		}
	}
	unrelatedGate := probe.DeepCopy()
	unrelatedGate.Spec.SchedulingGates[0].Name = "DifferentGate"
	mustCreate(t, admin, unrelatedGate)
	clearGate(unrelatedGate)
	if err := aaqClient.Update(ctx, unrelatedGate, client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), policyMessage) {
		t.Fatalf("unrelated gate removal must be denied: %v", err)
	}
	foreignProxy := probe.DeepCopy()
	foreignProxy.Name = "bedrock-db-proxy-elsewhere-7c9d6-test"
	foreignProxy.GenerateName = "bedrock-db-proxy-elsewhere-7c9d6-"
	foreignProxy.OwnerReferences[0].Name = "bedrock-db-proxy-elsewhere-7c9d6"
	mustCreate(t, admin, foreignProxy)
	clearGate(foreignProxy)
	if err := aaqClient.Update(ctx, foreignProxy, client.DryRunAll); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), policyMessage) {
		t.Fatalf("gate removal with inconsistent proxy identity must be denied: %v", err)
	}
	direct := probe.DeepCopy()
	direct.Name = "orders-endpoint-7c9d6-test"
	direct.GenerateName = "orders-endpoint-7c9d6-"
	direct.OwnerReferences[0].Name = "orders-endpoint-7c9d6"
	delete(direct.Labels, gateIdentity)
	mustCreate(t, admin, direct)
	clearGate(direct)
	if err := aaqClient.Update(ctx, direct); err != nil {
		t.Fatalf("quota controller could not release an ordinary database endpoint: %v", err)
	}
	allocate(direct)
	if err := fabricClient.Update(ctx, direct); err != nil {
		t.Fatalf("Fabric controller could not allocate an ordinary database endpoint: %v", err)
	}
	clearGate(pod)
	if err := aaqClient.Update(ctx, pod); err != nil {
		t.Fatalf("quota controller could not remove its gate: %v", err)
	}
	beforeAllocation := pod.DeepCopy()
	allocate(pod)
	if err := fabricClient.Patch(ctx, pod, client.MergeFrom(beforeAllocation)); err != nil {
		t.Fatalf("Fabric controller could not record both allocations: %v", err)
	}
	pod.Annotations[primary+"routed"] = "true"
	pod.Annotations[secondary+"routed"] = "true"
	if err := fabricClient.Update(ctx, pod); err != nil {
		t.Fatalf("Fabric controller could not record both routes: %v", err)
	}
	delete(pod.Annotations, secondary+"allocated")
	delete(pod.Annotations, secondary+"routed")
	if err := fabricClient.Update(ctx, pod); err != nil {
		t.Fatalf("Fabric controller could not clear stale allocation markers: %v", err)
	}
}
