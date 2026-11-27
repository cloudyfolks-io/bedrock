package database

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const (
	switchVIPType            = "switch_lb_vip"
	sourceProjectLabel       = "bedrock.cloudyfolks.com/project-id"
	sourceEnvironmentLabel   = "bedrock.cloudyfolks.com/environment"
	legacyLBManager          = "network-api"
	frontendChildPrefix      = "flb-"
	frontendServicePrefix    = "slr-"
	frontendOwnerLabel       = "fabric.cloudyfolks.io/loadbalancer"
	frontendVPCAnnotation    = "fabric.cloudyfolks.io/logical_router"
	frontendSubnetAnnotation = "fabric.cloudyfolks.io/logical_switch"
	frontendVIPAnnotation    = "fabric.cloudyfolks.io/switch_lb_vip"
)

type legacyEndpoint struct {
	settings        v1alpha1.DatabaseEndpointSpec
	vpc             string
	plane           string
	loadBalancerID  string
	projectID       string
	environment     string
	legacyVPC       string
	legacyNamespace string
	jobCIDR         string
	started         bool
	open            bool
}

func endpointObject(ctx context.Context, live client.Reader, gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := live.Get(ctx, client.ObjectKey{Name: name}, obj)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	return obj, err
}

func validateEndpointClaim(db v1alpha1.Database, subnet v1alpha1.Subnet, claim corev1.ConfigMap) *Refusal {
	spec := db.Spec.Endpoint
	if spec == nil || len(spec.LoadBalancerRef) > 55 || len(validation.IsDNS1123Label(spec.LoadBalancerRef)) > 0 || spec.Port < 1 || spec.Port > 65535 {
		return refusal(ReasonEndpointAddressInvalid, "the legacy load balancer name and TCP port must be valid")
	}
	addr, addrErr := netip.ParseAddr(spec.Address)
	prefix, prefixErr := netip.ParsePrefix(subnet.Spec.CIDR)
	if addrErr != nil || !addr.Is4() || prefixErr != nil || prefix.Contains(addr) {
		return refusal(ReasonEndpointAddressInvalid, "the legacy VIP must be an IPv4 address outside the proxy subnet")
	}
	data := claim.Data
	jobCIDR, jobCIDRErr := netip.ParsePrefix(data["jobCIDR"])
	if claim.Immutable == nil || !*claim.Immutable || claim.UID == "" || !claim.DeletionTimestamp.IsZero() ||
		db.UID == "" || data["uid"] != string(db.UID) || jobCIDRErr != nil || !jobCIDR.Addr().Is4() ||
		data["namespace"] != db.Namespace || data["database"] != db.Name || data["address"] != spec.Address ||
		data["port"] != strconv.Itoa(int(spec.Port)) || data["loadBalancerID"] == "" || data["projectID"] == "" || data["environment"] == "" ||
		subnet.UID == "" || data["subnetUID"] != string(subnet.UID) || subnet.Spec.NetworkRef == "" || data["network"] != subnet.Spec.NetworkRef {
		return refusal(ReasonEndpointAddressInvalid, "the legacy VIP needs an immutable endpoint claim for this database")
	}
	return nil
}

func approvedEndpointMarker(claim, marker corev1.ConfigMap) bool {
	if claim.UID == "" || marker.Immutable == nil || !*marker.Immutable || !marker.DeletionTimestamp.IsZero() ||
		marker.Data["claimUID"] != string(claim.UID) {
		return false
	}
	for _, key := range []string{"uid", "namespace", "database", "subnetUID", "network"} {
		if claim.Data[key] == "" || marker.Data[key] != claim.Data[key] {
			return false
		}
	}
	return true
}

func proxyDatabase(in *flow) v1alpha1.Database {
	db := *in.db
	if in.legacy != nil && !in.legacy.open {
		db.Spec.AllowedCIDRs = []string{in.legacy.jobCIDR}
	}
	return db
}

func (r *Reconciler) legacyEndpointClaim(ctx context.Context, db v1alpha1.Database, subnet v1alpha1.Subnet) (corev1.ConfigMap, *Refusal, error) {
	spec := db.Spec.Endpoint
	var claim corev1.ConfigMap
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: v1alpha1.DatabaseEndpointClaimName(spec.LoadBalancerRef)}, &claim)
	if apierrors.IsNotFound(err) {
		return claim, refusal(ReasonEndpointAddressInvalid, "the legacy VIP needs an endpoint claim"), nil
	}
	if err != nil {
		return claim, nil, err
	}
	if problem := validateEndpointClaim(db, subnet, claim); problem != nil {
		return claim, problem, nil
	}
	if !db.Spec.Suspended {
		started, err := r.legacyEndpointStageApproved(ctx, claim, v1alpha1.DatabaseEndpointStartName(spec.LoadBalancerRef))
		if err != nil {
			return claim, nil, err
		}
		if !started {
			return claim, refusal(ReasonProxyWaiting, "the legacy frontend needs administrator approval to start"), nil
		}
	}
	return claim, nil, nil
}

func (r *Reconciler) legacyEndpointStageApproved(ctx context.Context, claim corev1.ConfigMap, name string) (bool, error) {
	var marker corev1.ConfigMap
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: name}, &marker)
	return err == nil && approvedEndpointMarker(claim, marker), client.IgnoreNotFound(err)
}

func (r *Reconciler) legacyEndpointAuthorization(ctx context.Context, db v1alpha1.Database) (*Refusal, error) {
	subnet, problem, err := resolveSubnet(ctx, r.Live, db)
	if err != nil {
		return nil, err
	}
	if (problem != nil && problem.Reason != ReasonSubnetNotReady) || !subnet.DeletionTimestamp.IsZero() {
		return refusal(ReasonEndpointAddressInvalid, "the approved proxy subnet is missing or being deleted"), nil
	}
	claim, rejected, err := r.legacyEndpointClaim(ctx, db, subnet)
	if err != nil || rejected != nil {
		return rejected, err
	}
	_, rejected, err = r.legacyEndpointOpen(ctx, db, claim)
	return rejected, err
}

func ownedByDatabase(obj metav1.Object, db v1alpha1.Database) bool {
	owner := metav1.GetControllerOf(obj)
	return owner != nil && db.UID != "" && owner.UID == db.UID && owner.Name == db.Name && owner.Kind == Kind && owner.APIVersion == v1alpha1.GroupVersion.String()
}

func (r *Reconciler) legacyEndpointOpen(ctx context.Context, db v1alpha1.Database, claim corev1.ConfigMap) (bool, *Refusal, error) {
	open, err := r.legacyEndpointStageApproved(ctx, claim, v1alpha1.DatabaseEndpointOpenName(db.Spec.Endpoint.LoadBalancerRef))
	if open || err != nil {
		return open, nil, err
	}
	var group v1alpha1.SecurityGroup
	err = r.Live.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: SecurityGroupName(db.UID)}, &group)
	if client.IgnoreNotFound(err) != nil {
		return false, nil, err
	}
	if err == nil && group.DeletionTimestamp.IsZero() && ownedByDatabase(&group, db) {
		jobCIDR, _ := netip.ParsePrefix(claim.Data["jobCIDR"])
		ingress := group.Spec.Ingress
		if len(ingress) == 1 && validPort(int(ingress[0].Port)) && ingress[0] == databaseIngressRule(jobCIDR.Masked().String(), ingress[0].Port) {
			return false, nil, nil
		}
		if len(ingress) > 0 {
			return false, refusal(ReasonProxyWaiting, "the database endpoint needs administrator approval to reopen customer access"), nil
		}
	}
	active, err := r.legacyEndpointActive(ctx, db)
	if err != nil || !active {
		return false, nil, err
	}
	return false, refusal(ReasonProxyWaiting, "the active legacy endpoint has no approved job ingress security group"), nil
}

func (r *Reconciler) prepareLegacyEndpoint(ctx context.Context, in *flow) (*Refusal, error) {
	db := *in.db
	spec := db.Spec.Endpoint
	claim, rejected, err := r.legacyEndpointClaim(ctx, db, in.subnet)
	if err != nil || rejected != nil {
		if err == nil && !db.Spec.Suspended {
			_, err = r.withdrawLegacyEndpoint(ctx, db)
		}
		return rejected, err
	}
	started := !db.Spec.Suspended
	open := false
	if started {
		open, rejected, err = r.legacyEndpointOpen(ctx, db, claim)
		if err != nil || rejected != nil {
			if err == nil {
				_, err = r.withdrawLegacyEndpoint(ctx, db)
			}
			return rejected, err
		}
	}
	jobCIDR, _ := netip.ParsePrefix(claim.Data["jobCIDR"])

	var parent v1alpha1.Network
	err = r.Live.Get(ctx, client.ObjectKey{Name: in.subnet.Spec.NetworkRef}, &parent)
	if apierrors.IsNotFound(err) {
		return refusal(ReasonProxyWaiting, "the Network of the proxy subnet does not exist"), nil
	}
	if err != nil {
		return nil, err
	}
	if parent.Spec.TenantRef != in.project.Tenant {
		return refusal(ReasonEndpointAddressInvalid, "the Network of the proxy subnet belongs to another tenant"), nil
	}
	if parent.Status.VPC == "" || !meta.IsStatusConditionTrue(parent.Status.Conditions, v1alpha1.ConditionReady) {
		return refusal(ReasonProxyWaiting, "the Network of the proxy subnet is not ready"), nil
	}
	backend, err := endpointObject(ctx, r.Live, fabric.SubnetGVK, network.FabricName(db.Namespace, in.subnet.Name))
	if err != nil {
		return nil, err
	}
	if backend == nil || !backend.GetDeletionTimestamp().IsZero() || !fabric.SubnetReady(backend) {
		return refusal(ReasonProxyWaiting, "the Fabric proxy subnet is not ready"), nil
	}
	backendOwner, backendUID, owned := network.OwnerOf(backend)
	backendVPC, _, _ := unstructured.NestedString(backend.Object, "spec", "vpc")
	if !owned || backendOwner != client.ObjectKeyFromObject(&in.subnet) || backendUID != in.subnet.UID || backendVPC != parent.Status.VPC {
		return refusal(ReasonEndpointAddressInvalid, "the Fabric proxy subnet does not belong to the approved Network"), nil
	}

	plane := network.LBSubnetName(parent.Status.VPC)
	subnet, err := endpointObject(ctx, r.Live, fabric.SubnetGVK, plane)
	if err != nil {
		return nil, err
	}
	if subnet == nil || !subnet.GetDeletionTimestamp().IsZero() || !fabric.SubnetReady(subnet) {
		return refusal(ReasonProxyWaiting, "the load balancer subnet is not ready"), nil
	}
	planeOwner, planeUID, owned := network.OwnerOf(subnet)
	rawCIDR, _, _ := unstructured.NestedString(subnet.Object, "spec", "cidrBlock")
	planeVPC, _, _ := unstructured.NestedString(subnet.Object, "spec", "vpc")
	gateway, _, _ := unstructured.NestedString(subnet.Object, "spec", "gateway")
	excluded, _, _ := unstructured.NestedStringSlice(subnet.Object, "spec", "excludeIps")
	addressSpace := v1alpha1.Subnet{Spec: v1alpha1.SubnetSpec{CIDR: rawCIDR, Gateway: gateway, ExcludeIPs: excluded}}
	if !owned || planeOwner.Name != parent.Name || planeOwner.Namespace != "" || planeUID != parent.UID || planeVPC != parent.Status.VPC ||
		network.CheckNICAddress(addressSpace, spec.Address) != nil {
		return refusal(ReasonEndpointAddressInvalid, "the legacy VIP is outside the usable address range of the Network load balancer subnet"), nil
	}
	in.legacy = &legacyEndpoint{
		settings: *spec, vpc: parent.Status.VPC, plane: plane,
		loadBalancerID: claim.Data["loadBalancerID"], projectID: claim.Data["projectID"], environment: claim.Data["environment"],
		legacyVPC: claim.Data["legacyVPC"], legacyNamespace: claim.Data["legacyNamespace"],
		jobCIDR: jobCIDR.Masked().String(), started: started, open: open,
	}
	return nil, nil
}

func (e legacyEndpoint) vip(db v1alpha1.Database, project Project) *unstructured.Unstructured {
	name := Suffixed(db.UID, "frontend-vip")
	obj := fabric.Vip(fabric.Meta{Name: name}, db.Namespace, e.plane)
	spec := obj.Object["spec"].(map[string]any)
	spec["v4ip"], spec["type"] = e.settings.Address, switchVIPType
	return Adopt(obj, db, project)
}

func (e legacyEndpoint) loadBalancer(db v1alpha1.Database, project Project, proxy string, port int) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"vpc": e.vpc, "namespace": db.Namespace,
			"frontend": map[string]any{"vip": e.settings.Address},
			"selector": []any{}, "endpoints": []any{proxy},
			"ports": []any{map[string]any{"name": "database", "protocol": "TCP", "port": int64(e.settings.Port), "targetPort": int64(port)}},
		},
	}}
	obj.SetName(e.settings.LoadBalancerRef)
	obj.SetGroupVersionKind(fabric.LoadBalancerGVK)
	obj.SetAnnotations(map[string]string{fabric.AnnotationServiceHealthCheck: "false"})
	return Adopt(obj, db, project)
}

func (e legacyEndpoint) switchRule(db v1alpha1.Database, project Project, subnet v1alpha1.Subnet, proxy string, port int) *unstructured.Unstructured {
	metadata := fabric.Meta{Name: e.settings.LoadBalancerRef, Annotations: map[string]string{
		frontendVPCAnnotation: e.vpc, frontendSubnetAnnotation: network.FabricName(db.Namespace, subnet.Name), fabric.AnnotationServiceHealthCheck: "false",
	}}
	ports := []fabric.RouterLBPort{{Name: "database", Port: e.settings.Port, TargetPort: int32(port), Protocol: "TCP"}}
	return Adopt(fabric.SwitchLBRule(metadata, e.settings.Address, db.Namespace, []string{proxy}, ports), db, project)
}

func ownedFrontend(obj *unstructured.Unstructured, db v1alpha1.Database) bool {
	owner := platform.OwnerOf(obj)
	return owner.Kind == Kind && owner.Name == db.Name && owner.Namespace == db.Namespace && owner.UID == db.UID &&
		obj.GetLabels()[LabelManagedBy] == ManagedBy && db.UID != ""
}

func (e legacyEndpoint) isLegacyFrontend(obj *unstructured.Unstructured) bool {
	spec := e.settings
	labels := obj.GetLabels()
	if e.legacyVPC == "" || e.legacyNamespace == "" || platform.OwnerOf(obj).UID != "" || len(obj.GetOwnerReferences()) != 0 ||
		labels[LabelManagedBy] != legacyLBManager || labels[LabelInstance] != e.loadBalancerID ||
		labels[sourceProjectLabel] != e.projectID || labels[sourceEnvironmentLabel] != e.environment {
		return false
	}
	vpc, _, _ := unstructured.NestedString(obj.Object, "spec", "vpc")
	vip, _, _ := unstructured.NestedString(obj.Object, "spec", "frontend", "vip")
	eip, _, _ := unstructured.NestedString(obj.Object, "spec", "frontend", "ovnEip")
	if obj.GroupVersionKind() == fabric.SwitchLBRuleGVK {
		vpc = obj.GetAnnotations()[frontendVPCAnnotation]
		vip, _, _ = unstructured.NestedString(obj.Object, "spec", "vip")
	}
	ns, _, _ := unstructured.NestedString(obj.Object, "spec", "namespace")
	ports, _, _ := unstructured.NestedSlice(obj.Object, "spec", "ports")
	if vpc != e.legacyVPC || ns != e.legacyNamespace || vip != spec.Address || eip != "" || len(ports) != 1 {
		return false
	}
	port, ok := ports[0].(map[string]any)
	value, _, _ := unstructured.NestedInt64(port, "port")
	protocol, _, _ := unstructured.NestedString(port, "protocol")
	return ok && value == int64(spec.Port) && protocol == "TCP"
}

func frontendSpecMatches(obj, desired *unstructured.Unstructured) bool {
	have, want := obj.GetAnnotations(), desired.GetAnnotations()
	return reflect.DeepEqual(obj.Object["spec"], desired.Object["spec"]) &&
		have[fabric.AnnotationServiceHealthCheck] == want[fabric.AnnotationServiceHealthCheck] &&
		have[frontendVPCAnnotation] == want[frontendVPCAnnotation] && have[frontendSubnetAnnotation] == want[frontendSubnetAnnotation]
}

func (e legacyEndpoint) currentFrontend(ctx context.Context, live client.Reader) (*unstructured.Unstructured, *Refusal, error) {
	name := e.settings.LoadBalancerRef
	unified, err := endpointObject(ctx, live, fabric.LoadBalancerGVK, name)
	if err != nil {
		return nil, nil, err
	}
	standalone, err := endpointObject(ctx, live, fabric.SwitchLBRuleGVK, name)
	if err != nil {
		return nil, nil, err
	}
	if standalone != nil && unified != nil {
		return nil, refusal(ReasonEndpointAddressTaken, "both Fabric frontend kinds use the legacy load balancer name"), nil
	}
	if standalone != nil {
		return standalone, nil, nil
	}
	return unified, nil, nil
}

func (e legacyEndpoint) checkFrontendConflict(ctx context.Context, live client.Reader, have *unstructured.Unstructured) (*Refusal, error) {
	if have == nil || have.GroupVersionKind() == fabric.LoadBalancerGVK {
		router, err := endpointObject(ctx, live, fabric.RouterLBRuleGVK, frontendChildPrefix+e.settings.LoadBalancerRef)
		if err != nil {
			return nil, err
		}
		if router != nil {
			return refusal(ReasonEndpointAddressTaken, "a Fabric RouterLBRule uses the planned child name"), nil
		}
	}
	var balancers unstructured.UnstructuredList
	balancers.SetGroupVersionKind(fabric.LoadBalancerGVK.GroupVersion().WithKind("LoadBalancerList"))
	if err := live.List(ctx, &balancers); err != nil {
		return nil, err
	}
	for _, balancer := range balancers.Items {
		vip, _, _ := unstructured.NestedString(balancer.Object, "spec", "frontend", "vip")
		vpc, _, _ := unstructured.NestedString(balancer.Object, "spec", "vpc")
		if balancer.GetName() != e.settings.LoadBalancerRef && vip == e.settings.Address && (vpc == "" || vpc == e.vpc) {
			return refusal(ReasonEndpointAddressTaken, "another Fabric LoadBalancer uses the legacy VIP in the target VPC"), nil
		}
	}
	var rules unstructured.UnstructuredList
	rules.SetGroupVersionKind(fabric.SwitchLBRuleGVK.GroupVersion().WithKind("SwitchLBRuleList"))
	if err := live.List(ctx, &rules); err != nil {
		return nil, err
	}
	for _, rule := range rules.Items {
		annotations := rule.GetAnnotations()
		vpc := annotations[frontendVPCAnnotation]
		vip, _, _ := unstructured.NestedString(rule.Object, "spec", "vip")
		switch rule.GetName() {
		case e.settings.LoadBalancerRef:
			if have != nil && have.GroupVersionKind() == fabric.SwitchLBRuleGVK && rule.GetUID() == have.GetUID() {
				continue
			}
			return refusal(ReasonEndpointAddressTaken, "a foreign Fabric SwitchLBRule uses the legacy load balancer name"), nil
		case frontendChildPrefix + e.settings.LoadBalancerRef:
			if have != nil && have.GroupVersionKind() == fabric.LoadBalancerGVK && rule.GetLabels()[frontendOwnerLabel] == have.GetName() && vip == e.settings.Address {
				continue
			}
			return refusal(ReasonEndpointAddressTaken, "a foreign Fabric SwitchLBRule uses the planned child name"), nil
		}
		if vip == e.settings.Address && (vpc == "" || vpc == e.vpc) {
			return refusal(ReasonEndpointAddressTaken, "another Fabric SwitchLBRule uses the legacy VIP in the target VPC"), nil
		}
	}
	return nil, nil
}

func (e legacyEndpoint) checkServiceConflict(ctx context.Context, live client.Reader, db v1alpha1.Database, have *unstructured.Unstructured) (*Refusal, error) {
	name := frontendChildPrefix + e.settings.LoadBalancerRef
	if have != nil && have.GroupVersionKind() == fabric.SwitchLBRuleGVK {
		name = e.settings.LoadBalancerRef
	}
	key := client.ObjectKey{Namespace: db.Namespace, Name: frontendServicePrefix + name}
	var service corev1.Service
	err := live.Get(ctx, key, &service)
	if client.IgnoreNotFound(err) != nil {
		return nil, err
	}
	if err == nil {
		var previousNamespace, previousVPC string
		if have != nil {
			previousNamespace, _, _ = unstructured.NestedString(have.Object, "spec", "namespace")
			previousVPC, _, _ = unstructured.NestedString(have.Object, "spec", "vpc")
			if have.GroupVersionKind() == fabric.SwitchLBRuleGVK {
				previousVPC = have.GetAnnotations()[frontendVPCAnnotation]
			}
		}
		if previousNamespace != db.Namespace || previousVPC == "" || service.Annotations[frontendVPCAnnotation] != previousVPC ||
			service.Annotations[frontendVIPAnnotation] != e.settings.Address {
			return refusal(ReasonEndpointAddressTaken, "a Kubernetes Service uses the planned frontend name"), nil
		}
		return nil, nil
	}
	var endpoints corev1.Endpoints
	err = live.Get(ctx, key, &endpoints)
	if err == nil {
		return refusal(ReasonEndpointAddressTaken, "Kubernetes Endpoints use the planned frontend name without its Service"), nil
	}
	return nil, client.IgnoreNotFound(err)
}

func existingVIPMatches(have, want *unstructured.Unstructured, db v1alpha1.Database, address string) (bool, *Refusal) {
	if !ownedFrontend(have, db) || !matchesVIPReservation(have, want) {
		return false, refusal(ReasonEndpointAddressTaken, "the legacy VIP reservation belongs to another owner or has different settings")
	}
	got := fabric.VipAddress(have)
	if got != "" && got != address {
		return false, refusal(ReasonEndpointAddressTaken, "Fabric assigned a different address to the legacy VIP reservation")
	}
	mac, _, _ := unstructured.NestedString(have.Object, "status", "mac")
	return got != "" && mac != "" && have.GetDeletionTimestamp().IsZero(), nil
}

func (r *Reconciler) reserveLegacyVIP(ctx context.Context, in *flow) (bool, *Refusal, error) {
	want := in.legacy.vip(*in.db, in.project)
	have, err := endpointObject(ctx, r.Live, fabric.VipGVK, want.GetName())
	if err != nil {
		return false, nil, err
	}
	if have == nil {
		return false, nil, r.Client.Create(ctx, want, client.FieldOwner(v1alpha1.DatabaseFieldManager))
	}
	ready, rejected := existingVIPMatches(have, want, *in.db, in.legacy.settings.Address)
	return ready, rejected, nil
}

func matchesVIPReservation(have, want *unstructured.Unstructured) bool {
	for _, field := range []string{"namespace", "subnet", "type", "v4ip"} {
		wantValue, _, _ := unstructured.NestedString(want.Object, "spec", field)
		got, _, _ := unstructured.NestedString(have.Object, "spec", field)
		if wantValue != got {
			return false
		}
	}
	return true
}

func (r *Reconciler) applyLegacyFrontend(ctx context.Context, in *flow, address string) (*Refusal, error) {
	e := in.legacy
	if e == nil || !e.started || in.db.Spec.Suspended {
		return nil, nil
	}
	ready, err := r.readyLegacyProxy(ctx, *in.db, in.subnet, address)
	if err != nil || !ready {
		return nil, err
	}
	have, rejected, err := e.currentFrontend(ctx, r.Live)
	if err != nil || rejected != nil {
		return rejected, err
	}
	owned := have != nil && ownedFrontend(have, *in.db)
	if have != nil && (!have.GetDeletionTimestamp().IsZero() || !owned && !e.isLegacyFrontend(have)) {
		return refusal(ReasonEndpointAddressTaken, "the existing Fabric frontend does not match the approved legacy owner and address"), nil
	}
	want := e.loadBalancer(*in.db, in.project, address, in.port)
	if have != nil && have.GroupVersionKind() == fabric.SwitchLBRuleGVK {
		want = e.switchRule(*in.db, in.project, in.subnet, address, in.port)
	}
	if !owned || !frontendSpecMatches(have, want) {
		if conflict, err := e.checkFrontendConflict(ctx, r.Live, have); err != nil || conflict != nil {
			return conflict, err
		}
		if conflict, err := e.checkServiceConflict(ctx, r.Live, *in.db, have); err != nil || conflict != nil {
			return conflict, err
		}
	}
	ready, rejected, err = r.reserveLegacyVIP(ctx, in)
	if err != nil || rejected != nil || !ready {
		return rejected, err
	}
	if have == nil {
		return nil, r.Client.Create(ctx, want, client.FieldOwner(v1alpha1.DatabaseFieldManager))
	}
	if owned && frontendSpecMatches(have, want) {
		return nil, nil
	}
	next := Adopt(have, *in.db, in.project)
	next.Object["spec"] = want.Object["spec"]
	annotations := next.GetAnnotations()
	for key, value := range want.GetAnnotations() {
		annotations[key] = value
	}
	next.SetAnnotations(annotations)
	return nil, r.Client.Patch(ctx, next, client.MergeFromWithOptions(have, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
}

func (r *Reconciler) readyLegacyProxy(ctx context.Context, db v1alpha1.Database, subnet v1alpha1.Subnet, address string) (bool, error) {
	deployment := &unstructured.Unstructured{}
	deployment.SetGroupVersionKind(DeploymentGVK)
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: EndpointName(db)}, deployment)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil || !metav1.IsControlledBy(deployment, &db) || !deploymentReady(deployment) {
		return false, err
	}
	fabricSubnet := network.FabricName(db.Namespace, subnet.Name)
	ips, err := network.LiveIPsOfAddress(ctx, r.Live, fabricSubnet, address)
	if err != nil {
		return false, err
	}
	if len(ips) != 1 || !ips[0].GetDeletionTimestamp().IsZero() {
		return false, nil
	}
	namespace, podName, observedSubnet := fabric.IPOwner(ips[0])
	if namespace != db.Namespace || podName == "" || observedSubnet != fabricSubnet || fabric.IPAddress(ips[0]) != address {
		return false, nil
	}
	var pod corev1.Pod
	err = r.Live.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod)
	if err != nil {
		return false, client.IgnoreNotFound(err)
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.Kind != "ReplicaSet" || !pod.DeletionTimestamp.IsZero() || !network.PodReady(pod) ||
		pod.Labels[managedProxyLabel] != string(db.UID) || pod.Labels[network.NetworkClassLabel] != network.NetworkClassManaged {
		return false, nil
	}
	var replicaSet appsv1.ReplicaSet
	if err := r.Live.Get(ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, &replicaSet); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return replicaSet.UID == owner.UID && metav1.IsControlledBy(&replicaSet, deployment), nil
}

func (r *Reconciler) legacyFrontendReady(ctx context.Context, in *flow) (bool, error) {
	e := in.legacy
	address, ok := proxyAddressOf(*in.db, in.subnet.Name)
	if e == nil || !e.started || in.db.Spec.Suspended || !ok {
		return false, nil
	}
	ready, err := r.readyLegacyProxy(ctx, *in.db, in.subnet, address)
	if err != nil || !ready {
		return false, err
	}
	have, conflict, err := e.currentFrontend(ctx, r.Live)
	if err != nil || conflict != nil || have == nil || !ownedFrontend(have, *in.db) || !have.GetDeletionTimestamp().IsZero() {
		return false, err
	}
	name := have.GetName()
	want := e.switchRule(*in.db, in.project, in.subnet, address, in.port)
	if have.GroupVersionKind() == fabric.LoadBalancerGVK {
		name = frontendChildPrefix + name
		want = e.loadBalancer(*in.db, in.project, address, in.port)
	}
	if !frontendSpecMatches(have, want) {
		return false, nil
	}
	wantVIP := e.vip(*in.db, in.project)
	haveVIP, err := endpointObject(ctx, r.Live, fabric.VipGVK, wantVIP.GetName())
	if err != nil || haveVIP == nil {
		return false, err
	}
	if ready, _ := existingVIPMatches(haveVIP, wantVIP, *in.db, e.settings.Address); !ready {
		return false, nil
	}
	service := in.db.Namespace + "/" + frontendServicePrefix + name
	served, _, _ := unstructured.NestedString(have.Object, "status", "service")
	ports, _, _ := unstructured.NestedString(have.Object, "status", "ports")
	if served != service || ports != strconv.Itoa(int(e.settings.Port))+"/TCP" {
		return false, nil
	}
	if have.GroupVersionKind() == fabric.LoadBalancerGVK {
		vip, _, _ := unstructured.NestedString(have.Object, "status", "vip")
		if vip != e.settings.Address {
			return false, nil
		}
		child, err := endpointObject(ctx, r.Live, fabric.SwitchLBRuleGVK, name)
		if err != nil || child == nil {
			return false, err
		}
		childIP, _, _ := unstructured.NestedString(child.Object, "spec", "vip")
		childNamespace, _, _ := unstructured.NestedString(child.Object, "spec", "namespace")
		childEndpoints, _, _ := unstructured.NestedStringSlice(child.Object, "spec", "endpoints")
		childPorts, _, _ := unstructured.NestedSlice(child.Object, "spec", "ports")
		wantPorts, _, _ := unstructured.NestedSlice(want.Object, "spec", "ports")
		if child.GetLabels()[frontendOwnerLabel] != want.GetName() || child.GetAnnotations()[frontendVPCAnnotation] != e.vpc ||
			child.GetAnnotations()[fabric.AnnotationServiceHealthCheck] != "false" ||
			childIP != vip || childNamespace != in.db.Namespace || !reflect.DeepEqual(childPorts, wantPorts) ||
			len(childEndpoints) != 1 || childEndpoints[0] != address || fabric.SwitchLBRuleService(child) != service || !child.GetDeletionTimestamp().IsZero() {
			return false, nil
		}
	}
	key := client.ObjectKey{Namespace: in.db.Namespace, Name: frontendServicePrefix + name}
	var targetService corev1.Service
	if err := r.Live.Get(ctx, key, &targetService); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if !targetService.DeletionTimestamp.IsZero() || targetService.Annotations[frontendVIPAnnotation] != e.settings.Address ||
		targetService.Annotations[frontendVPCAnnotation] != e.vpc || targetService.Annotations[fabric.AnnotationServiceHealthCheck] != "false" ||
		len(targetService.Spec.Ports) != 1 || len(targetService.Spec.Selector) != 0 {
		return false, nil
	}
	listener := targetService.Spec.Ports[0]
	if listener.Protocol != corev1.ProtocolTCP || listener.Port != e.settings.Port || listener.TargetPort.IntVal != int32(in.port) {
		return false, nil
	}
	var endpoints corev1.Endpoints
	err = r.Live.Get(ctx, key, &endpoints)
	if err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return hasFrontendTarget(endpoints, address, int32(in.port)), nil
}

func hasFrontendTarget(endpoints corev1.Endpoints, address string, port int32) bool {
	if !endpoints.DeletionTimestamp.IsZero() || len(endpoints.Subsets) != 1 {
		return false
	}
	subset := endpoints.Subsets[0]
	return len(subset.Addresses) == 1 && subset.Addresses[0].IP == address && len(subset.NotReadyAddresses) == 0 &&
		len(subset.Ports) == 1 && subset.Ports[0].Port == port && subset.Ports[0].Protocol == corev1.ProtocolTCP
}

func (r *Reconciler) legacyEndpointActive(ctx context.Context, db v1alpha1.Database) (bool, error) {
	var proxy appsv1.Deployment
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: EndpointName(db)}, &proxy)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}
	if err == nil && ownedByDatabase(&proxy, db) && (ptr.Deref(proxy.Spec.Replicas, 1) != 0 || proxy.Status.Replicas != 0 || proxy.Status.ObservedGeneration < proxy.Generation) {
		return true, nil
	}
	for _, kind := range []schema.GroupVersionKind{fabric.LoadBalancerGVK, fabric.SwitchLBRuleGVK} {
		obj, err := endpointObject(ctx, r.Live, kind, db.Spec.Endpoint.LoadBalancerRef)
		if err != nil {
			return false, err
		}
		if obj != nil && ownedFrontend(obj, db) {
			return true, nil
		}
	}
	return r.legacyProxyHasActivePods(ctx, db)
}

func (r *Reconciler) withdrawLegacyEndpoint(ctx context.Context, db v1alpha1.Database) (bool, error) {
	proxyStopped, proxyErr := r.stopLegacyProxy(ctx, db)
	frontendGone, frontendErr := r.removeLegacyFrontend(ctx, db)
	return proxyStopped && frontendGone, errors.Join(proxyErr, frontendErr)
}

func (r *Reconciler) stopLegacyProxy(ctx context.Context, db v1alpha1.Database) (bool, error) {
	var proxy appsv1.Deployment
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: EndpointName(db)}, &proxy)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}
	if err == nil && proxy.DeletionTimestamp.IsZero() && ownedByDatabase(&proxy, db) {
		if ptr.Deref(proxy.Spec.Replicas, 1) != 0 {
			before := proxy.DeepCopy()
			proxy.Spec.Replicas = ptr.To[int32](0)
			return false, r.Client.Patch(ctx, &proxy, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
		}
		if proxy.Status.ObservedGeneration < proxy.Generation || proxy.Status.Replicas != 0 {
			return false, nil
		}
	}
	pods, err := r.legacyProxyHasActivePods(ctx, db)
	return err == nil && !pods, err
}

func (r *Reconciler) legacyProxyHasActivePods(ctx context.Context, db v1alpha1.Database) (bool, error) {
	var pods corev1.PodList
	if err := r.Live.List(ctx, &pods, client.InNamespace(db.Namespace), client.MatchingLabels{managedProxyLabel: string(db.UID)}); err != nil {
		return false, err
	}
	if pods.Continue != "" {
		return false, errors.New("the managed proxy pod list is incomplete")
	}
	for i := range pods.Items {
		if !legacyProxyPodStopped(&pods.Items[i]) {
			return true, nil
		}
	}
	return false, nil
}

func legacyProxyPodStopped(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if (condition.Type == corev1.PodReady || condition.Type == corev1.ContainersReady) && condition.Status == corev1.ConditionTrue {
			return false
		}
	}
	if len(pod.Spec.Containers) == 0 || len(pod.Spec.Containers) != len(pod.Status.ContainerStatuses) ||
		len(pod.Spec.InitContainers) != len(pod.Status.InitContainerStatuses) ||
		len(pod.Spec.EphemeralContainers) != len(pod.Status.EphemeralContainerStatuses) {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if !legacyProxyContainerStopped(container.Name, pod.Status.ContainerStatuses) {
			return false
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if !legacyProxyContainerStopped(container.Name, pod.Status.InitContainerStatuses) {
			return false
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if !legacyProxyContainerStopped(container.Name, pod.Status.EphemeralContainerStatuses) {
			return false
		}
	}
	return true
}

func legacyProxyContainerStopped(name string, statuses []corev1.ContainerStatus) bool {
	stopped := false
	for _, status := range statuses {
		if status.Name != name {
			continue
		}
		state := status.State
		if stopped || status.Ready || state.Running != nil || state.Waiting != nil || state.Terminated == nil ||
			state.Terminated.FinishedAt.IsZero() || state.Terminated.Reason == "ContainerStatusUnknown" {
			return false
		}
		stopped = true
	}
	return stopped
}

func (r *Reconciler) removeLegacyFrontend(ctx context.Context, db v1alpha1.Database) (bool, error) {
	if !hasLegacyEndpoint(db) {
		return true, nil
	}
	name := db.Spec.Endpoint.LoadBalancerRef
	checks := []struct {
		kind        schema.GroupVersionKind
		serviceName string
	}{
		{kind: fabric.LoadBalancerGVK, serviceName: frontendServicePrefix + frontendChildPrefix + name},
		{kind: fabric.SwitchLBRuleGVK, serviceName: frontendServicePrefix + name},
	}
	allGone := true
	for _, check := range checks {
		obj, err := endpointObject(ctx, r.Live, check.kind, name)
		if err != nil {
			return false, err
		}
		if obj != nil {
			if ownedFrontend(obj, db) {
				allGone = false
				if err := r.deleteFrontendObject(ctx, obj); err != nil {
					return false, err
				}
			}
			continue
		}
		if check.kind == fabric.LoadBalancerGVK {
			child, err := endpointObject(ctx, r.Live, fabric.SwitchLBRuleGVK, frontendChildPrefix+name)
			if err != nil {
				return false, err
			}
			if child != nil && child.GetLabels()[frontendOwnerLabel] == name {
				ns, _, _ := unstructured.NestedString(child.Object, "spec", "namespace")
				vip, _, _ := unstructured.NestedString(child.Object, "spec", "vip")
				if ns == db.Namespace && vip == db.Spec.Endpoint.Address {
					allGone = false
				}
			}
		}
		var service corev1.Service
		key := client.ObjectKey{Namespace: db.Namespace, Name: check.serviceName}
		if err := r.Live.Get(ctx, key, &service); client.IgnoreNotFound(err) != nil {
			return false, err
		}
		if service.Annotations[frontendVIPAnnotation] == db.Spec.Endpoint.Address {
			allGone = false
		}
	}
	return allGone, nil
}

func (r *Reconciler) deleteFrontendObject(ctx context.Context, obj *unstructured.Unstructured) error {
	if !obj.GetDeletionTimestamp().IsZero() {
		return nil
	}
	uid, version := obj.GetUID(), obj.GetResourceVersion()
	return client.IgnoreNotFound(r.Client.Delete(ctx, obj, client.Preconditions{UID: &uid, ResourceVersion: &version}))
}

func (r *Reconciler) releaseLegacyVIP(ctx context.Context, db v1alpha1.Database) (bool, error) {
	if !hasLegacyEndpoint(db) {
		return true, nil
	}
	name := Suffixed(db.UID, "frontend-vip")
	obj, err := endpointObject(ctx, r.Live, fabric.VipGVK, name)
	if err != nil || obj == nil || !ownedFrontend(obj, db) {
		return err == nil, err
	}
	if err := r.deleteFrontendObject(ctx, obj); err != nil {
		return false, fmt.Errorf("release the legacy VIP reservation: %w", err)
	}
	return false, nil
}
