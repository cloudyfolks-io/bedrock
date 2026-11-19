package tenancy

import (
	"context"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	serviceTenantNamespace = "service-project"
	serviceFabricUser      = "system:serviceaccount:kube-system:ovn"
	serviceOwnerUser       = "bedrock:service-owner"
	serviceTenantAccount   = "system:serviceaccount:service-project:builder"
	serviceVPC             = "fabric.cloudyfolks.io/vpc"
	serviceSwitchVIP       = "fabric.cloudyfolks.io/switch_lb_vip"
	servicePolicyName      = "bedrock-tenant-services"
)

func tenantService(namespace, name string, annotations map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: annotations},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "test"}, Ports: []corev1.ServicePort{{Port: 80}}},
	}
}

func requireServiceDenied(t *testing.T, err error, message string) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected denial containing %q, got %v", message, err)
	}
}

func TestTenantServiceRoutingAdmission(t *testing.T) {
	ctx := context.Background()
	admin, cfg := startEnv(t)
	applyFile(t, admin, manifestPath("86-tenancy", "10-project-roles.yaml"))
	ownerBinding := editorIn(serviceTenantNamespace, serviceOwnerUser)
	ownerBinding.RoleRef.Name = "bedrock-project-owner"
	mustCreate(t, admin,
		namespaceNamed(serviceTenantNamespace, map[string]string{"bedrock.cloudyfolks.io/tenant": "test"}),
		namespaceNamed("other-service-namespace", nil),
		ownerBinding, editorIn(serviceTenantNamespace, serviceTenantAccount),
		editorIn(serviceTenantNamespace, serviceFabricUser), editorIn("other-service-namespace", serviceOwnerUser),
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "service-test-admin", Namespace: serviceTenantNamespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "bedrock-project-owner"},
			Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "bedrock:bedrock-admins"}},
		},
	)
	owner := actingAs(t, cfg, serviceOwnerUser)
	builder := actingAs(t, cfg, serviceTenantAccount)
	fabric := actingAs(t, cfg, serviceFabricUser)
	platform := actingAs(t, cfg, "bedrock:service-admin", "bedrock:bedrock-admins")
	applyFile(t, admin, manifestPath("86-tenancy", "87-tenant-service-policy.yaml"))
	probe := tenantService(serviceTenantNamespace, "probe", map[string]string{serviceSwitchVIP: "192.0.2.12"})
	requireServiceDenied(t, eventuallyForbidden(t, denyCreate(admin, owner, probe)), "can manage a Fabric frontend Service")
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	if err := admin.Get(ctx, client.ObjectKey{Name: servicePolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	if warnings := typeCheckWarnings(t, cfg, policy); len(warnings) != 0 {
		t.Fatalf("policy expression warnings: %v", warnings)
	}

	t.Run("ordinary services and controller annotations", func(t *testing.T) {
		ordinary := tenantService(serviceTenantNamespace, "ordinary", nil)
		ordinary.Spec.Type = corev1.ServiceTypeLoadBalancer
		ordinary.Spec.LoadBalancerClass = ptr.To("fabric.cloudyfolks.io/loadbalancer")
		mustCreate(t, owner, ordinary)
		ordinary.Annotations = map[string]string{serviceVPC: "test-vpc"}
		if err := fabric.Update(ctx, ordinary); err != nil {
			t.Fatal(err)
		}
		changed := ordinary.DeepCopy()
		changed.Annotations[serviceVPC] = "other-vpc"
		requireServiceDenied(t, owner.Update(ctx, changed), "can change Fabric routing annotations")
		ordinary.Annotations["example.test/owner"] = "project"
		ordinary.Spec.Selector = map[string]string{"app": "replacement"}
		if err := owner.Update(ctx, ordinary); err != nil {
			t.Fatal(err)
		}
		if err := owner.Delete(ctx, ordinary); err != nil {
			t.Fatal(err)
		}
		mustCreate(t, owner, tenantService("other-service-namespace", "service-outside-project", map[string]string{serviceVPC: "test-vpc"}))
	})

	t.Run("routing fields are protected for tenant users and service accounts", func(t *testing.T) {
		for _, key := range []string{
			"fabric.cloudyfolks.io/logical_router", "fabric.cloudyfolks.io/logical_switch", serviceVPC,
			"fabric.cloudyfolks.io/service_health_check", "fabric.cloudyfolks.io/service_external_ip_from_subnet",
			"fabric.cloudyfolks.io/attachmentprovider", "lb.fabric.cloudyfolks.io/address-pool",
			"lb.fabric.cloudyfolks.io/ips", "lb.fabric.cloudyfolks.io/allow-shared-ip",
		} {
			t.Run(key, func(t *testing.T) {
				service := tenantService(serviceTenantNamespace, "routing-field", map[string]string{key: "value"})
				requireServiceDenied(t, owner.Create(ctx, service), "can change Fabric routing annotations")
				requireServiceDenied(t, builder.Create(ctx, service), "can change Fabric routing annotations")
			})
		}
	})

	t.Run("generated names and objects belong to Fabric", func(t *testing.T) {
		for _, prefix := range []string{"slr-", "rlr-"} {
			requireServiceDenied(t, builder.Create(ctx, tenantService(serviceTenantNamespace, prefix+"reserved", nil)), "name prefixes are reserved")
			generated := tenantService(serviceTenantNamespace, "", nil)
			generated.GenerateName = prefix + "reserved-"
			requireServiceDenied(t, builder.Create(ctx, generated), "name prefixes are reserved")
		}
		managed := tenantService(serviceTenantNamespace, "slr-controller-service", map[string]string{serviceSwitchVIP: "192.0.2.12"})
		managed.Labels = map[string]string{"test-collection": "managed"}
		mustCreate(t, fabric, managed)
		changed := managed.DeepCopy()
		changed.Annotations = nil
		requireServiceDenied(t, owner.Update(ctx, changed), "can manage a Fabric frontend Service")
		requireServiceDenied(t, owner.Delete(ctx, managed), "can manage a Fabric frontend Service")
		requireServiceDenied(t, owner.DeleteAllOf(ctx, &corev1.Service{}, client.InNamespace(serviceTenantNamespace), client.MatchingLabels{"test-collection": "managed"}), "can manage a Fabric frontend Service")
		if err := platform.Delete(ctx, managed); err != nil {
			t.Fatal(err)
		}
	})
}
