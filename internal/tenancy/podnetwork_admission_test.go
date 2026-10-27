package tenancy

import (
	"context"
	"fmt"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/cel/openapi/resolver"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	podNetworkUser     = "bedrock:podnet"
	podNetworkSubnet   = "acme-default-0123456789ab"
	foreignSubnet      = "other-default-ba9876543210"
	podNetworkGroup    = "acme-web-0123456789ab"
	foreignGroup       = "other-web-ba9876543210"
	podNetworkReplicas = "system:serviceaccount:kube-system:replicaset-controller"
	podNetworkPlatform = "system:serviceaccount:kubevirt:kubevirt-controller"
	netOneDomain       = "vpc.kube-system.fabric.cloudyfolks.io/"
	ethZeroDomain      = "fabric.cloudyfolks.io/"
)

var podNetworkPolicyNames = []string{"bedrock-tenant-pod-network", "bedrock-tenant-pod-network-subnet", "bedrock-tenant-pod-network-groups"}

func goodAnnotations(class string) map[string]string {
	eth0 := "tenant-managed"
	if class == "workload" {
		eth0 = "tenant-workload"
	}
	return map[string]string{
		"k8s.v1.cni.cncf.io/networks":    "kube-system/vpc",
		ethZeroDomain + "logical_switch": eth0,
		ethZeroDomain + "port_security":  "true",
		netOneDomain + "logical_switch":  podNetworkSubnet,
		netOneDomain + "port_security":   "true",
		netOneDomain + "security_groups": podNetworkGroup + ",default-acme-shop",
	}
}

type podChange func(*corev1.Pod)

func tenantPod(name, class string, changes ...podChange) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "acme-shop",
			Labels:      map[string]string{"network.cloudyfolks.com/class": class},
			Annotations: goodAnnotations(class),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: pauseImage}}},
	}
	if class == "workload" {
		pod.Spec.AutomountServiceAccountToken = ptr.To(false)
	}
	for _, change := range changes {
		change(pod)
	}
	return pod
}

func withAnnotation(key, value string) podChange {
	return func(p *corev1.Pod) { p.Annotations[key] = value }
}

func withoutAnnotation(key string) podChange {
	return func(p *corev1.Pod) { delete(p.Annotations, key) }
}

func withLabel(key, value string) podChange {
	return func(p *corev1.Pod) { p.Labels[key] = value }
}

func withAddedCapability(name string) podChange {
	return func(p *corev1.Pod) {
		p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{corev1.Capability(name)}}}
	}
}

type podNetworkWorld struct {
	admin, user, platform, replicaset client.Client
	cfg                               *rest.Config
}

func fabricOwnedBy(object client.Object, namespace string) client.Object {
	object.SetAnnotations(map[string]string{"bedrock.cloudyfolks.io/owner-namespace": namespace})
	return object
}

func newPodNetworkWorld(t *testing.T) podNetworkWorld {
	t.Helper()
	admin, cfg := startEnv(t)
	applyFile(t, admin, manifestPath("86-tenancy", "40-workload-policies.yaml"))
	applyFile(t, admin, manifestPath("86-tenancy", "85-tenant-pod-network-policy.yaml"))
	mustCreate(t, admin,
		namespaceNamed("acme-shop", map[string]string{"bedrock.cloudyfolks.io/tenant": "acme"}),
		namespaceNamed("plain", nil),
		fabricOwnedBy(fabric.Subnet(fabric.Meta{Name: podNetworkSubnet}, fabric.SubnetSpec{CIDR: "10.100.0.0/24", Gateway: "10.100.0.1"}), "acme-shop"),
		fabricOwnedBy(fabric.Subnet(fabric.Meta{Name: foreignSubnet}, fabric.SubnetSpec{CIDR: "10.200.0.0/24", Gateway: "10.200.0.1"}), "other-shop"),
		fabricOwnedBy(fabric.SecurityGroup(fabric.Meta{Name: podNetworkGroup}, nil, nil), "acme-shop"),
		fabricOwnedBy(fabric.SecurityGroup(fabric.Meta{Name: foreignGroup}, nil, nil), "other-shop"),
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-writer"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods", "pods/ephemeralcontainers"}, Verbs: []string{"create", "update", "patch", "get", "delete"}}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-writer"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "pod-writer"},
			Subjects: []rbacv1.Subject{
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: podNetworkUser},
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: podNetworkReplicas},
				{APIGroup: rbacv1.GroupName, Kind: "User", Name: podNetworkPlatform},
			},
		},
	)
	defaultGroup := fabric.SecurityGroup(fabric.Meta{Name: "default-acme-shop", Labels: map[string]string{v1alpha1.LabelKind: "Namespace", v1alpha1.LabelName: "acme-shop"}}, nil, nil)
	mustCreate(t, admin, defaultGroup)
	return podNetworkWorld{
		admin: admin, cfg: cfg,
		user:       actingAs(t, cfg, podNetworkUser),
		platform:   actingAs(t, cfg, podNetworkPlatform),
		replicaset: actingAs(t, cfg, podNetworkReplicas),
	}
}

func (w podNetworkWorld) setMode(t *testing.T, mode string) {
	t.Helper()
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: SettingPodNetworkAdmission}}
	err := w.admin.Get(ctx, client.ObjectKeyFromObject(setting), setting)
	setting.Spec.Value = mode
	if apierrors.IsNotFound(err) {
		err = w.admin.Create(ctx, setting)
	} else if err == nil {
		err = w.admin.Update(ctx, setting)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&PodNetworkAdmissionReconciler{Client: w.admin}).Reconcile(ctx, podNetworkAdmissionRequest); err != nil {
		t.Fatal(err)
	}
}

func (w podNetworkWorld) bindingNames(t *testing.T) []string {
	t.Helper()
	var list admissionregistrationv1.ValidatingAdmissionPolicyBindingList
	if err := w.admin.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, binding := range list.Items {
		if isPodNetworkBinding(binding.Name) {
			names = append(names, binding.Name)
		}
	}
	return names
}

func (w podNetworkWorld) waitDenying(t *testing.T) {
	t.Helper()
	probe := tenantPod("probe", "workload", withLabel("network.cloudyfolks.com/class", "nonsense"))
	if err := eventuallyForbidden(t, denyCreate(w.admin, w.user, probe)); err == nil {
		t.Fatal("the pod network policy never denied")
	}
}

func typeCheckWarnings(t *testing.T, cfg *rest.Config, policy *admissionregistrationv1.ValidatingAdmissionPolicy) []admissionregistrationv1.ExpressionWarning {
	t.Helper()
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	checker := &validating.TypeChecker{
		SchemaResolver: &resolver.ClientDiscoveryResolver{Discovery: discoveryClient},
		RestMapper:     restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient)),
	}
	return checker.Check(policy)
}

func TestPodNetworkPoliciesTypeCheckWithoutWarnings(t *testing.T) {
	w := newPodNetworkWorld(t)
	for _, name := range podNetworkPolicyNames {
		var policy admissionregistrationv1.ValidatingAdmissionPolicy
		if err := w.admin.Get(context.Background(), client.ObjectKey{Name: name}, &policy); err != nil {
			t.Fatal(err)
		}
		if warnings := typeCheckWarnings(t, w.cfg, &policy); len(warnings) > 0 {
			t.Fatalf("%s type check warnings: %+v", name, warnings)
		}
	}
}

func TestPodNetworkTypeCheckFindsAWrongExpression(t *testing.T) {
	w := newPodNetworkWorld(t)
	var policy admissionregistrationv1.ValidatingAdmissionPolicy
	if err := w.admin.Get(context.Background(), client.ObjectKey{Name: "bedrock-tenant-pod-network"}, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.Validations = append(policy.Spec.Validations, admissionregistrationv1.Validation{Expression: "object.spec.noSuchField == 'x'"})
	if len(typeCheckWarnings(t, w.cfg, &policy)) == 0 {
		t.Fatal("the checker accepted an unknown field, so it proves nothing")
	}
}

func TestPodNetworkAdmissionIsOffByDefault(t *testing.T) {
	w := newPodNetworkWorld(t)
	if names := w.bindingNames(t); len(names) != 0 {
		t.Fatalf("bindings %v", names)
	}
	if _, err := (&PodNetworkAdmissionReconciler{Client: w.admin}).Reconcile(context.Background(), podNetworkAdmissionRequest); err != nil {
		t.Fatal(err)
	}
	if names := w.bindingNames(t); len(names) != 0 {
		t.Fatalf("bindings after a reconcile %v", names)
	}
	mustCreate(t, w.user, tenantPod("legacy", "nonsense", withoutAnnotation("k8s.v1.cni.cncf.io/networks")))
}

func TestPodNetworkDenyAdmitsGoodPodsAndDeniesBadOnes(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	mustCreate(t, w.user, tenantPod("good-workload", "workload"), tenantPod("good-managed", "managed"))

	bad := map[string]struct {
		pod     *corev1.Pod
		message string
	}{
		"no class":                    {tenantPod("a", "workload", withLabel("network.cloudyfolks.com/class", "")), "must be workload or managed"},
		"wrong networks":              {tenantPod("b", "workload", withAnnotation("k8s.v1.cni.cncf.io/networks", "kube-system/vpc,other/x")), "kube-system/vpc"},
		"missing networks":            {tenantPod("c", "workload", withoutAnnotation("k8s.v1.cni.cncf.io/networks")), "kube-system/vpc"},
		"eth0 on the managed subnet":  {tenantPod("d", "workload", withAnnotation(ethZeroDomain+"logical_switch", "tenant-managed")), "eth0 logical_switch"},
		"eth0 on the workload subnet": {tenantPod("e", "managed", withAnnotation(ethZeroDomain+"logical_switch", "tenant-workload")), "eth0 logical_switch"},
		"eth0 port security off":      {tenantPod("f", "workload", withAnnotation(ethZeroDomain+"port_security", "false")), "port_security"},
		"net1 port security off":      {tenantPod("g", "managed", withAnnotation(netOneDomain+"port_security", "false")), "port_security"},
		"net1 port security missing":  {tenantPod("h", "managed", withoutAnnotation(netOneDomain+"port_security")), "port_security"},
		"net1 subnet missing":         {tenantPod("i", "managed", withoutAnnotation(netOneDomain+"logical_switch")), "net1 logical_switch"},
		"net1 subnet of another":      {tenantPod("j", "managed", withAnnotation(netOneDomain+"logical_switch", foreignSubnet)), "net1 logical_switch"},
		"net1 group of another":       {tenantPod("k", "managed", withAnnotation(netOneDomain+"security_groups", podNetworkGroup+","+foreignGroup)), "net1 security_groups"},
		"workload with a token":       {tenantPod("l", "workload", func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }), "automountServiceAccountToken"},
		"workload with NET_BIND":      {tenantPod("m", "workload", withAddedCapability("NET_BIND_SERVICE")), "NET_BIND_SERVICE"},
	}
	for name, c := range bad {
		err := w.user.Create(context.Background(), c.pod)
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%s: err = %v, want forbidden with %q", name, err, c.message)
		}
	}
}

func TestPodNetworkManagedPodsKeepTheirTokenAndMayAddCapabilities(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	mustCreate(t, w.user, tenantPod("managed", "managed", func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }, withAddedCapability("NET_BIND_SERVICE")))
}

func TestPodNetworkDefaultSecurityGroupBelongsToTheProject(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	mustCreate(t, w.user, tenantPod("default-group", "managed", withAnnotation(netOneDomain+"security_groups", "default-acme-shop")))
}

func TestPodNetworkAuditNeverDenies(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	w.setMode(t, "audit")
	bad := func(name string) *corev1.Pod {
		return tenantPod(name, "workload", withAnnotation(netOneDomain+"logical_switch", foreignSubnet), withAnnotation(ethZeroDomain+"port_security", "false"))
	}
	waitFor(t, func() bool { return w.user.Create(context.Background(), bad("audited-first")) == nil })
	for i := range 5 {
		mustCreate(t, w.user, bad(fmt.Sprintf("audited-%d", i)))
	}
}

func TestPodNetworkOffRemovesTheBindings(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	if names := w.bindingNames(t); len(names) != 3 {
		t.Fatalf("bindings %v", names)
	}
	w.setMode(t, "off")
	if names := w.bindingNames(t); len(names) != 0 {
		t.Fatalf("bindings %v", names)
	}
	waitFor(t, func() bool { return w.user.Create(context.Background(), tenantPod("after-off", "nonsense")) == nil })
}

func TestPodNetworkLabelUpdateOfAnExistingPodIsAdmitted(t *testing.T) {
	w := newPodNetworkWorld(t)
	legacy := plainPod("legacy", "acme-shop", keep)
	mustCreate(t, w.admin, legacy)
	w.setMode(t, "deny")
	w.waitDenying(t)
	ctx := context.Background()
	var pod corev1.Pod
	if err := w.user.Get(ctx, client.ObjectKeyFromObject(legacy), &pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels = map[string]string{"app": "renamed"}
	if err := w.user.Update(ctx, &pod); err != nil {
		t.Fatalf("a label update must pass: %v", err)
	}
}

func TestPodNetworkEphemeralContainersAreChecked(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	mustCreate(t, w.user, tenantPod("debuggee", "workload"))
	ctx := context.Background()
	var pod corev1.Pod
	if err := w.user.Get(ctx, client.ObjectKey{Namespace: "acme-shop", Name: "debuggee"}, &pod); err != nil {
		t.Fatal(err)
	}
	debug := corev1.EphemeralContainer{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
		Name: "debug", Image: pauseImage,
		SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_BIND_SERVICE"}}},
	}}
	pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{debug}
	err := w.user.SubResource("ephemeralcontainers").Update(ctx, &pod)
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "NET_BIND_SERVICE") {
		t.Fatalf("err = %v", err)
	}
	pod.Spec.EphemeralContainers[0].SecurityContext = nil
	if err := w.user.SubResource("ephemeralcontainers").Update(ctx, &pod); err != nil {
		t.Fatalf("a plain ephemeral container must pass: %v", err)
	}
}

func TestPodNetworkPolicyLeavesPlatformIdentitiesAndOtherNamespacesAlone(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	mustCreate(t, w.platform, plainPod("launcher", "acme-shop", keep))
	mustCreate(t, w.user, plainPod("elsewhere", "plain", keep))
	err := w.replicaset.Create(context.Background(), plainPod("from-controller", "acme-shop", keep))
	requireDenied(t, err, "network.cloudyfolks.com/class")
}

func TestPodNetworkPolicyAddsNoSecondDenialForOtherCapabilities(t *testing.T) {
	w := newPodNetworkWorld(t)
	w.setMode(t, "deny")
	w.waitDenying(t)
	err := w.user.Create(context.Background(), tenantPod("sys-admin", "workload", withAddedCapability("SYS_ADMIN")))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "added capabilities other than NET_BIND_SERVICE") {
		t.Fatalf("bedrock-tenant-pods must deny it: %v", err)
	}
	if strings.Contains(err.Error(), "class workload containers must not add NET_BIND_SERVICE") {
		t.Fatalf("the pod network policy must not deny it a second time: %v", err)
	}
}
