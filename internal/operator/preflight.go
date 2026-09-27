package operator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/roles"
)

const (
	allowShutdownAnnotation = "bedrock.cloudyfolks.io/allow-shutdown"
	certificateMargin       = 7 * 24 * time.Hour
	kubeletReservePercent   = 15
)

var (
	vmiGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}
	vmGVK  = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"}
)

type preflightInput struct {
	Running   string
	To        string
	Target    *v1alpha1.Release
	Hosts     []v1alpha1.Host
	Nodes     []corev1.Node
	Ceph      cephReport
	CephError string
	VMIs      []unstructured.Unstructured
	VMs       []unstructured.Unstructured
	Now       time.Time
}

func preflight(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	in, err := gatherPreflight(ctx, env, cluster)
	if err != nil {
		return phaseResult{}, err
	}
	if problem := preflightProblem(in); problem != "" {
		return phaseResult{Blocked: problem}, nil
	}
	return phaseResult{Done: true}, nil
}

func gatherPreflight(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (preflightInput, error) {
	to := cluster.Status.Upgrade.To
	target, err := optionalRelease(ctx, env.Client, to)
	if err != nil {
		return preflightInput{}, err
	}
	var hosts v1alpha1.HostList
	if err := env.Client.List(ctx, &hosts); err != nil {
		return preflightInput{}, err
	}
	var nodes corev1.NodeList
	if err := env.Client.List(ctx, &nodes); err != nil {
		return preflightInput{}, err
	}
	vmis, err := listKind(ctx, env.Client, vmiGVK)
	if err != nil {
		return preflightInput{}, err
	}
	vms, err := listKind(ctx, env.Client, vmGVK)
	if err != nil {
		return preflightInput{}, err
	}
	ceph, cephErr := readCeph(ctx, env.Client, env.Exec)
	return preflightInput{
		Running:   cluster.Status.Version,
		To:        to,
		Target:    target,
		Hosts:     hosts.Items,
		Nodes:     nodes.Items,
		Ceph:      ceph,
		CephError: errorText(cephErr),
		VMIs:      vmis,
		VMs:       vms,
		Now:       time.Now(),
	}, nil
}

func optionalRelease(ctx context.Context, c client.Client, name string) (*v1alpha1.Release, error) {
	var found v1alpha1.Release
	err := c.Get(ctx, client.ObjectKey{Name: name}, &found)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func listKind(ctx context.Context, c client.Client, gvk schema.GroupVersionKind) ([]unstructured.Unstructured, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	err := c.List(ctx, &list)
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	return list.Items, err
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func preflightProblem(in preflightInput) string {
	checks := []func(preflightInput) []string{releaseProblems, depotProblems, nodeProblems, spareProblems, hostProblems, etcdProblems, cephProblems, vmProblems}
	var problems []string
	for _, check := range checks {
		problems = append(problems, check(in)...)
	}
	return strings.Join(problems, "; ")
}

func etcdProblems(in preflightInput) []string {
	if problem := etcdProblem(in.Hosts); problem != "" {
		return []string{problem}
	}
	return nil
}

func releaseProblems(in preflightInput) []string {
	if in.Target == nil {
		return []string{fmt.Sprintf("release: Release/%s does not exist", in.To)}
	}
	var problems []string
	if ready := meta.FindStatusCondition(in.Target.Status.Conditions, v1alpha1.ConditionReady); ready != nil && ready.Status == metav1.ConditionFalse {
		problems = append(problems, fmt.Sprintf("release: Release/%s is not ready: %s", in.To, ready.Message))
	}
	if !v1alpha1.ReleaseAllowsUpgrade(*in.Target, in.Running) {
		problems = append(problems, fmt.Sprintf("release: %s does not list %s in upgradeFrom", in.To, in.Running))
	}
	return problems
}

func depotProblems(in preflightInput) []string {
	var problems []string
	for _, arch := range nodeArchitectures(in.Nodes) {
		if _, ok := depotBundle(in.Hosts, in.To, arch); !ok {
			problems = append(problems, fmt.Sprintf("depot: no host serves %s for %s", in.To, arch))
		}
	}
	return problems
}

func nodeArchitectures(nodes []corev1.Node) []string {
	var arches []string
	for _, node := range nodes {
		if arch := node.Status.NodeInfo.Architecture; !slices.Contains(arches, arch) {
			arches = append(arches, arch)
		}
	}
	slices.Sort(arches)
	return arches
}

func depotBundle(hosts []v1alpha1.Host, version, arch string) (v1alpha1.DepotBundle, bool) {
	var largest v1alpha1.DepotBundle
	found := false
	for _, host := range hosts {
		if host.Status.Depot == nil {
			continue
		}
		for _, bundle := range host.Status.Depot.Bundles {
			if bundle.Version == version && bundle.Arch == arch && (!found || bundle.Bytes > largest.Bytes) {
				largest, found = bundle, true
			}
		}
	}
	return largest, found
}

func largestBundleBytes(hosts []v1alpha1.Host, version string) int64 {
	var largest int64
	for _, host := range hosts {
		if host.Status.Depot == nil {
			continue
		}
		for _, bundle := range host.Status.Depot.Bundles {
			if bundle.Version == version {
				largest = max(largest, bundle.Bytes)
			}
		}
	}
	return largest
}

func findNode(nodes []corev1.Node, name string) (corev1.Node, bool) {
	for _, node := range nodes {
		if node.Name == name {
			return node, true
		}
	}
	return corev1.Node{}, false
}

func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func nodeProblems(in preflightInput) []string {
	var problems []string
	for _, node := range in.Nodes {
		if !nodeReady(node) {
			problems = append(problems, fmt.Sprintf("nodes: %s is not Ready", node.Name))
		}
	}
	for _, host := range in.Hosts {
		if node, ok := findNode(in.Nodes, host.Name); ok && host.Status.Hostname != node.Name {
			problems = append(problems, fmt.Sprintf("nodes: host %s reports hostname %q, its Node is %s", host.Name, host.Status.Hostname, node.Name))
		}
	}
	return problems
}

func spareProblems(in preflightInput) []string {
	blocked := slices.DeleteFunc(slices.Clone(in.Nodes), schedulable)
	count := len(in.Nodes) - len(blocked)
	if len(in.Nodes) < 2 || count >= 2 {
		return nil
	}
	var actions []string
	for _, node := range blocked {
		actions = append(actions, blockedNodeActions(node)...)
	}
	return []string{fmt.Sprintf("nodes: a drain needs 2 schedulable Nodes, found %d of %d: %s, or join another Node with the workload role", count, len(in.Nodes), strings.Join(actions, ", "))}
}

func blockedNodeActions(node corev1.Node) []string {
	cordoned := node.Spec.Unschedulable
	needsWorkloadRole := false
	var otherTaints []string
	for _, taint := range node.Spec.Taints {
		if !blocksScheduling(taint) {
			continue
		}
		switch taint.Key {
		case corev1.TaintNodeUnschedulable:
			cordoned = true
		case roles.NoWorkloadTaint:
			needsWorkloadRole = true
		default:
			otherTaints = append(otherTaints, taint.ToString())
		}
	}
	var actions []string
	if cordoned {
		actions = append(actions, node.Name+": uncordon it")
	}
	if needsWorkloadRole {
		actions = append(actions, node.Name+": give its Host the workload role")
	}
	for _, taint := range otherTaints {
		actions = append(actions, node.Name+": remove the taint "+taint)
	}
	return actions
}

func hostProblems(in preflightInput) []string {
	var problems []string
	for _, host := range in.Hosts {
		problems = append(problems, hostCheckProblems(in, host)...)
	}
	return problems
}

func hostCheckProblems(in preflightInput, host v1alpha1.Host) []string {
	checks := host.Status.Checks
	if checks == nil {
		return []string{fmt.Sprintf("hosts: %s reports no checks", host.Name)}
	}
	var problems []string
	switch {
	case checks.CertificatesNotAfter == nil:
		problems = append(problems, fmt.Sprintf("certificates: %s reports no expiry", host.Name))
	case !checks.CertificatesNotAfter.After(in.Now.Add(certificateMargin)):
		problems = append(problems, fmt.Sprintf("certificates: %s expires %s, less than 7 days away", host.Name, checks.CertificatesNotAfter.UTC().Format(time.RFC3339)))
	}
	if !checks.TimeSynced {
		problems = append(problems, fmt.Sprintf("time: %s clock is not synchronized", host.Name))
	}
	if need := diskNeed(in, host); checks.VarLibFreeBytes < need {
		problems = append(problems, fmt.Sprintf("disk: %s has %s free in /var/lib, needs %s", host.Name, gibibytes(checks.VarLibFreeBytes), gibibytes(need)))
	}
	return problems
}

func diskNeed(in preflightInput, host v1alpha1.Host) int64 {
	checks := host.Status.Checks
	need := 2*hostBundleBytes(in, host) + kubeletReserve(in, host)
	if backup, ok := backupHost(in.Hosts); ok && backup.Name == host.Name {
		need += 2 * checks.ImagesBytes
	}
	return need
}

func kubeletReserve(in preflightInput, host v1alpha1.Host) int64 {
	if _, ok := findNode(in.Nodes, host.Name); !ok {
		return 0
	}
	return host.Status.Checks.VarLibSizeBytes * kubeletReservePercent / 100
}

func hostBundleBytes(in preflightInput, host v1alpha1.Host) int64 {
	node, ok := findNode(in.Nodes, host.Name)
	if !ok {
		return largestBundleBytes(in.Hosts, in.To)
	}
	bundle, _ := depotBundle(in.Hosts, in.To, node.Status.NodeInfo.Architecture)
	return bundle.Bytes
}

func gibibytes(bytes int64) string {
	return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(int64(1)<<30))
}

func cephProblems(in preflightInput) []string {
	if in.CephError != "" {
		return []string{"ceph: " + in.CephError}
	}
	if problem := cephHealthProblem(in.Ceph); problem != "" {
		return []string{problem}
	}
	return nil
}

func vmProblems(in preflightInput) []string {
	var problems []string
	for _, instance := range in.VMIs {
		migratable, reason := liveMigratable(instance)
		if migratable || allowsShutdown(in.VMs, instance.GetNamespace(), instance.GetName()) {
			continue
		}
		problems = append(problems, fmt.Sprintf("vms: %s/%s cannot live-migrate (%s) and lacks %s=true", instance.GetNamespace(), instance.GetName(), reason, allowShutdownAnnotation))
	}
	return problems
}

func liveMigratable(instance unstructured.Unstructured) (bool, string) {
	conditions, _, _ := unstructured.NestedSlice(instance.Object, "status", "conditions")
	for _, item := range conditions {
		condition, _ := item.(map[string]any)
		if condition["type"] == "LiveMigratable" {
			reason, _ := condition["reason"].(string)
			return condition["status"] == "True", reason
		}
	}
	return false, "no LiveMigratable condition"
}

func allowsShutdown(vms []unstructured.Unstructured, namespace, name string) bool {
	for _, vm := range vms {
		if vm.GetNamespace() == namespace && vm.GetName() == name {
			return vm.GetAnnotations()[allowShutdownAnnotation] == "true"
		}
	}
	return false
}
