package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	rookMonSecretName = "rook-ceph-mon"
	rookFSIDKey       = "fsid"
)

func adoptPhase(cluster v1alpha1.Cluster) string {
	if cluster.Spec.Storage.Adopt == nil {
		return ""
	}
	condition := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionStorageAdopted)
	if condition == nil {
		return ""
	}
	return condition.Reason
}

func adoptedMonFSID(ctx context.Context, c client.Reader, cluster v1alpha1.Cluster) (string, error) {
	if cluster.Spec.Storage.Adopt == nil {
		return "", nil
	}
	secret, err := optionalSecret(ctx, c, client.ObjectKey{Namespace: storageNamespace, Name: rookMonSecretName})
	return string(secretData(secret, rookFSIDKey)), err
}

func renderAdopted(in AddonInput, adopt v1alpha1.AdoptSpec) (Rendered, error) {
	hosts := osdHosts(in.Hosts)
	phase := adoptPhase(in.Cluster)
	if phase != v1alpha1.ReasonAdopted {
		if reason, message := adoptWait(in.CephMonFSID, adopt, osdNodeNames(in.Hosts)); reason != "" {
			return Rendered{SkipReason: reason, SkipMessage: message, Conditions: []metav1.Condition{adoptedCondition(reason, message)}}, nil
		}
	}
	if len(hosts) == 0 {
		return noDevices(), nil
	}
	image, replicas, err := storageSettings(in)
	if err != nil {
		return Rendered{}, err
	}
	cluster, err := adoptedCephCluster(image, hosts, adopt, replicas)
	if err != nil {
		return Rendered{}, err
	}
	switch phase {
	case v1alpha1.ReasonAdopted:
		objects := slices.Concat([]*unstructured.Unstructured{cluster}, storageObjects(replicas, hosts))
		return Rendered{Objects: objects, Probes: []Probe{cephClusterProbe(adoptedGate(cephGate(deviceCount(hosts)), in.CephProblems))}}, nil
	case "", v1alpha1.ReasonWaitingForIdentity, v1alpha1.ReasonWaitingForHosts:
		quorum := adoptedCondition(v1alpha1.ReasonWaitingForQuorum, "the CephCluster is rendered and waits for every monitor to join the quorum")
		return adoptionPending(cluster, v1alpha1.ReasonWaitingForQuorum, quorum), nil
	default:
		return adoptionPending(cluster, phase), nil
	}
}

func adoptionPending(cluster *unstructured.Unstructured, phase string, conditions ...metav1.Condition) Rendered {
	gate := func(*unstructured.Unstructured) release.Readiness {
		return release.Readiness{Reason: phase, Message: fmt.Sprintf("adoption is at %s", phase)}
	}
	return Rendered{Objects: []*unstructured.Unstructured{cluster}, Probes: []Probe{cephClusterProbe(gate)}, Conditions: conditions}
}

func adoptedCondition(reason, message string) metav1.Condition {
	status := metav1.ConditionFalse
	if reason == v1alpha1.ReasonAdopted {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{Type: v1alpha1.ConditionStorageAdopted, Status: status, Reason: reason, Message: message}
}

func adoptWait(monFSID string, adopt v1alpha1.AdoptSpec, osdNodes []string) (string, string) {
	if message := identityProblem(monFSID, adopt.FSID); message != "" {
		return v1alpha1.ReasonWaitingForIdentity, message
	}
	if missing := monitorsWithoutHost(adopt.Monitors, osdNodes); len(missing) > 0 {
		return v1alpha1.ReasonWaitingForHosts, "monitor hosts without the ceph-osd role: " + strings.Join(missing, ", ")
	}
	return "", ""
}

func identityProblem(monFSID, fsid string) string {
	switch {
	case monFSID == "":
		return fmt.Sprintf("Secret %s/%s does not exist or has no fsid", storageNamespace, rookMonSecretName)
	case monFSID != fsid:
		return fmt.Sprintf("Secret %s/%s holds another fsid than storage.adopt.fsid", storageNamespace, rookMonSecretName)
	}
	return ""
}

func monitorsWithoutHost(monitors []v1alpha1.AdoptMonitor, osdNodes []string) []string {
	var missing []string
	for _, monitor := range monitors {
		if !slices.Contains(osdNodes, monitor.Node) {
			missing = append(missing, monitor.Node)
		}
	}
	return missing
}

func adoptedCephCluster(image string, hosts []v1alpha1.Host, adopt v1alpha1.AdoptSpec, replicas int) (*unstructured.Unstructured, error) {
	mons := int64(len(adopt.Monitors))
	spec := cephClusterSpec(image, hosts, mons, replicas)
	spec["network"] = map[string]any{"provider": "host"}
	spec["mon"] = map[string]any{"count": mons, "allowMultiplePerNode": false}
	spec["healthCheck"] = map[string]any{"muteHealthWarning": adoptedHealthWarnings(replicas)}
	if adopt.Cephx != nil && len(adopt.Cephx.Raw) > 0 {
		var cephx map[string]any
		if err := utiljson.Unmarshal(adopt.Cephx.Raw, &cephx); err != nil {
			return nil, fmt.Errorf("storage.adopt.cephx: %w", err)
		}
		spec["security"] = map[string]any{"cephx": cephx}
	}
	return object("ceph.rook.io/v1", "CephCluster", storageNamespace, storageNamespace, spec), nil
}

func adoptedHealthWarnings(replicas int) map[string]any {
	muted := mutedHealthWarnings(replicas)
	muted["AUTH_EMERGENCY_CIPHERS_SET"] = map[string]any{"policy": "mute"}
	return muted
}

const verificationFailed = "VerificationFailed"

var (
	cephReleasePattern = regexp.MustCompile(`^ceph version ((\d+)[\d.]*)`)
	imageTagPattern    = regexp.MustCompile(`^v?(\d+)\.\d+`)
)

type cephTree struct {
	Nodes []struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Children []int  `json:"children"`
	} `json:"nodes"`
}

func adoptedProblems(ctx context.Context, c client.Client, exec execFunc, in AddonInput) []string {
	if exec == nil || in.Cluster.Spec.Storage.Adopt == nil || adoptPhase(in.Cluster) != v1alpha1.ReasonAdopted {
		return nil
	}
	image, replicas, err := storageSettings(in)
	if err != nil {
		return nil
	}
	pod, err := rookOperatorPod(ctx, c)
	if err != nil {
		return []string{safeMessage(err.Error())}
	}
	run := operatorRun(exec, pod)
	nodes := osdNodeNames(in.Hosts)
	reads := []func() ([]string, error){
		func() ([]string, error) { return versionsProblems(ctx, run, image) },
		func() ([]string, error) {
			return verified(ctx, run, "ceph osd pool ls detail", cephArgs("osd", "pool", "ls", "detail", "-f", "json"), func(pools []cephPoolState) []string {
				return poolSizeMismatches(replicas, pools)
			})
		},
		func() ([]string, error) {
			return verified(ctx, run, "ceph osd tree", cephArgs("osd", "tree", "-f", "json"), func(tree cephTree) []string {
				return hostBucketMismatches(nodes, tree)
			})
		},
		func() ([]string, error) { return statusProblems(ctx, run) },
	}
	var problems []string
	for _, read := range reads {
		found, err := read()
		if err != nil {
			return append(problems, err.Error())
		}
		problems = append(problems, found...)
	}
	return problems
}

func verified[T any](ctx context.Context, run runFunc, what string, command []string, check func(T) []string) ([]string, error) {
	var data T
	if err := readJSON(ctx, run, &data, command); err != nil {
		return nil, unreadable(what, err)
	}
	return check(data), nil
}

func unreadable(what string, err error) error {
	return fmt.Errorf("cannot read %s: %s", what, safeMessage(err.Error()))
}

func versionsProblems(ctx context.Context, run runFunc, image string) ([]string, error) {
	major, tagged := imageMajor(image)
	if !tagged {
		return []string{fmt.Sprintf("ceph versions: the image %q has no release tag", image)}, nil
	}
	return verified(ctx, run, "ceph versions", cephArgs("versions", "-f", "json"), func(sections map[string]map[string]int) []string {
		return releaseMismatches(major, sections)
	})
}

func imageMajor(image string) (string, bool) {
	reference, _, _ := strings.Cut(image, "@")
	_, tag, found := strings.Cut(reference[strings.LastIndex(reference, "/")+1:], ":")
	match := imageTagPattern.FindStringSubmatch(tag)
	if !found || match == nil {
		return "", false
	}
	return match[1], true
}

func releaseMismatches(major string, sections map[string]map[string]int) []string {
	var behind []string
	for _, section := range slices.Sorted(maps.Keys(sections)) {
		if section == "overall" {
			continue
		}
		for _, version := range slices.Sorted(maps.Keys(sections[section])) {
			number, daemonMajor := cephRelease(version)
			if daemonMajor != major {
				behind = append(behind, section+" "+number)
			}
		}
	}
	if len(behind) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("ceph versions: not on release %s: %s", major, strings.Join(behind, ", "))}
}

func cephRelease(version string) (string, string) {
	match := cephReleasePattern.FindStringSubmatch(version)
	if match == nil {
		return version, ""
	}
	return match[1], match[2]
}

func bedrockPool(name string) bool {
	return name == blockPoolName || strings.HasPrefix(name, filesystemName+"-") || strings.HasPrefix(name, objectStoreName+".")
}

func poolSizeMismatches(replicas int, pools []cephPoolState) []string {
	var off []string
	for _, pool := range pools {
		if bedrockPool(pool.Name) && pool.Size != replicas {
			off = append(off, fmt.Sprintf("ceph pool %s size %d, want %d", pool.Name, pool.Size, replicas))
		}
	}
	return off
}

func osdNodeNames(hosts []v1alpha1.Host) []string {
	var names []string
	for _, host := range hosts {
		if slices.Contains(host.Spec.Roles, v1alpha1.RoleCephOSD) {
			names = append(names, host.Name)
		}
	}
	return names
}

func hostBucketMismatches(nodes []string, tree cephTree) []string {
	kinds := map[int]string{}
	for _, node := range tree.Nodes {
		kinds[node.ID] = node.Type
	}
	var strays []string
	for _, node := range tree.Nodes {
		holdsOSD := slices.ContainsFunc(node.Children, func(id int) bool { return kinds[id] == "osd" })
		if node.Type == "host" && holdsOSD && !slices.Contains(nodes, node.Name) {
			strays = append(strays, node.Name)
		}
	}
	if len(strays) == 0 {
		return nil
	}
	return []string{"ceph osd tree: host buckets without a ceph-osd node: " + strings.Join(strays, ", ")}
}

func statusProblems(ctx context.Context, run runFunc) ([]string, error) {
	out, err := run(ctx, cephArgs("status", "-f", "json"))
	if err != nil {
		return nil, unreadable("ceph status", err)
	}
	report, err := parseCephStatus(out)
	if err != nil {
		return nil, unreadable("ceph status", err)
	}
	return slices.DeleteFunc([]string{cephStableProblem(report), cephMisplacedProblem(report)}, func(problem string) bool { return problem == "" }), nil
}

func adoptedGate(gate release.Gate, problems []string) release.Gate {
	if len(problems) == 0 {
		return gate
	}
	return func(obj *unstructured.Unstructured) release.Readiness {
		readiness := gate(obj)
		if readiness.Ready {
			return release.Readiness{Reason: verificationFailed, Message: strings.Join(problems, "; ")}
		}
		readiness.Message = strings.Join(slices.Concat([]string{readiness.Message}, problems), "; ")
		return readiness
	}
}

const (
	rgwRootPool     = ".rgw.root"
	cephStepTimeout = 2 * time.Minute
	messageLines    = 3
	messageRunes    = 512

	zonePoolScript = `set -e
zone=$(radosgw-admin zone get --rgw-zone="$2" "$3")
printf '%s' "$zone" | sed "s/\"$1\\.rgw\\./\"$2.rgw./g" | radosgw-admin zone set --rgw-zone="$2" --infile=/dev/stdin "$3" >/dev/null`

	periodCommitScript = `radosgw-admin period update --commit --rgw-realm="$1" "$2" >/dev/null`

	zoneStalePoolsScript = `set -e
zone=$(radosgw-admin zone get --rgw-zone="$2" "$3")
printf '%s' "$zone" | grep -o "\"$1\\.rgw\\.[^\"]*\"" || [ $? -eq 1 ]`
)

var (
	errZonePools    = errors.New("cannot read the pools of the zone")
	adoptStepPhases = []string{
		v1alpha1.ReasonWaitingForQuorum,
		v1alpha1.ReasonWaitingForOSDs,
		v1alpha1.ReasonRenaming,
		v1alpha1.ReasonPoolSizeTooSmall,
		v1alpha1.ReasonRenameFailed,
	}
)

type runFunc func(ctx context.Context, command []string) ([]byte, error)

type renameCommand struct {
	Name string
	Args []string
}

type cephPoolState struct {
	Name string `json:"pool_name"`
	Size int    `json:"size"`
}

type cephFilesystemState struct {
	Name                string   `json:"name"`
	MetadataPool        string   `json:"metadata_pool"`
	DataPools           []string `json:"data_pools"`
	Joinable            bool     `json:"-"`
	RefuseClientSession bool     `json:"-"`
}

type cephState struct {
	Quorum         []string
	UpOSDs         int
	Pools          []cephPoolState
	Filesystems    []cephFilesystemState
	Realms         []string
	ZoneGroups     []string
	Zones          []string
	ZoneStalePools []string
}

type poolMove struct {
	from, to string
}

func advanceAdoption(ctx context.Context, c client.Client, exec execFunc, in AddonInput) []metav1.Condition {
	adopt := in.Cluster.Spec.Storage.Adopt
	if adopt == nil || !slices.Contains(adoptStepPhases, adoptPhase(in.Cluster)) {
		return nil
	}
	replicas, err := replicaCount(in.Settings[storageReplicasKey])
	if err != nil {
		return nil
	}
	pod, err := rookOperatorPod(ctx, c)
	if err != nil {
		return []metav1.Condition{adoptedCondition(v1alpha1.ReasonWaitingForQuorum, safeMessage(err.Error()))}
	}
	return []metav1.Condition{adoptStep(ctx, operatorRun(exec, pod), *adopt, replicas)}
}

func adoptStep(ctx context.Context, run runFunc, adopt v1alpha1.AdoptSpec, replicas int) metav1.Condition {
	if message := v1alpha1.AdoptPoolNameProblem(adopt.Pools); message != "" {
		return adoptedCondition(v1alpha1.ReasonRenameFailed, message)
	}
	state, err := readCephState(ctx, run, adopt)
	if errors.Is(err, errZonePools) {
		return adoptedCondition(v1alpha1.ReasonRenameFailed, safeMessage(err.Error()))
	}
	if err != nil {
		return adoptedCondition(v1alpha1.ReasonWaitingForQuorum, "cannot read the Ceph state: "+safeMessage(err.Error()))
	}
	if reason, message := healthProblem(state, adopt); reason != "" {
		return adoptedCondition(reason, message)
	}
	if message := poolSizeProblem(state, adopt, replicas); message != "" {
		return adoptedCondition(v1alpha1.ReasonPoolSizeTooSmall, message)
	}
	if message := missingAdopted(state, adopt); message != "" {
		return adoptedCondition(v1alpha1.ReasonRenameFailed, message)
	}
	if message := renameConflict(state, adopt); message != "" {
		return adoptedCondition(v1alpha1.ReasonRenameFailed, message)
	}
	plan := renamePlan(state, adopt)
	if len(plan) == 0 {
		return adoptedCondition(v1alpha1.ReasonAdopted, "the pools, the CephFS and the RGW realm have the Bedrock names")
	}
	for _, command := range plan {
		if _, err := run(ctx, command.Args); err != nil {
			return adoptedCondition(v1alpha1.ReasonRenameFailed, command.Name+": "+safeMessage(err.Error()))
		}
	}
	return adoptedCondition(v1alpha1.ReasonRenaming, fmt.Sprintf("ran %d rename commands, the next reconcile checks the result", len(plan)))
}

func operatorRun(exec execFunc, pod string) runFunc {
	return func(ctx context.Context, command []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, cephStepTimeout)
		defer cancel()
		out, err := exec(ctx, storageNamespace, pod, rookOperatorContainer, command)
		if err != nil {
			return nil, errors.New(exitMessage(err))
		}
		return out, nil
	}
}

func exitMessage(err error) string {
	if inner := errors.Unwrap(err); inner != nil {
		return inner.Error()
	}
	return err.Error()
}

func safeMessage(message string) string {
	var kept []string
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(strings.ToLower(line), "key") {
			kept = append(kept, line)
		}
	}
	runes := []rune(strings.Join(kept[max(0, len(kept)-messageLines):], "; "))
	return string(runes[:min(len(runes), messageRunes)])
}

func healthProblem(state cephState, adopt v1alpha1.AdoptSpec) (string, string) {
	var missing []string
	for _, monitor := range adopt.Monitors {
		if !slices.Contains(state.Quorum, monitor.ID) {
			missing = append(missing, monitor.ID)
		}
	}
	if len(missing) > 0 {
		return v1alpha1.ReasonWaitingForQuorum, "monitors not in quorum: " + strings.Join(missing, ", ")
	}
	devices := 0
	for _, host := range adopt.Hosts {
		devices += len(host.Devices)
	}
	if state.UpOSDs < devices {
		return v1alpha1.ReasonWaitingForOSDs, fmt.Sprintf("%d of %d OSDs are up", state.UpOSDs, devices)
	}
	return "", ""
}

func poolSizeProblem(state cephState, adopt v1alpha1.AdoptSpec, replicas int) string {
	var large []string
	for _, pool := range state.Pools {
		if pool.Size > replicas && adoptedPool(state, adopt, pool.Name) {
			large = append(large, fmt.Sprintf("%s (%d)", pool.Name, pool.Size))
		}
	}
	if len(large) == 0 {
		return ""
	}
	return fmt.Sprintf("pools larger than the replicas: %s; storage.replicas is %d", strings.Join(large, ", "), replicas)
}

func missingAdopted(state cephState, adopt v1alpha1.AdoptSpec) string {
	pools := adopt.Pools
	hasPool := func(match func(string) bool) bool {
		return slices.ContainsFunc(state.Pools, func(pool cephPoolState) bool { return match(pool.Name) })
	}
	named := func(names []string, old string) bool {
		return slices.Contains(names, old) || slices.Contains(names, objectStoreName)
	}
	_, filesystem := findFilesystem(state, pools.Filesystem)
	checks := []struct {
		kind, configured, label string
		found                   bool
	}{
		{"block pool", pools.Block, pools.Block, hasPool(func(name string) bool { return name == pools.Block || name == blockPoolName })},
		{"filesystem", pools.Filesystem, pools.Filesystem, filesystem},
		{"realm", pools.ObjectStore, pools.ObjectStore, named(state.Realms, pools.ObjectStore)},
		{"zonegroup", pools.ObjectStore, pools.ObjectStore, named(state.ZoneGroups, pools.ObjectStore)},
		{"zone", pools.ObjectStore, pools.ObjectStore, named(state.Zones, pools.ObjectStore)},
		{"object store pools", pools.ObjectStore, pools.ObjectStore + ".rgw.*", hasPool(func(name string) bool {
			return strings.HasPrefix(name, pools.ObjectStore+".rgw.") || strings.HasPrefix(name, objectStoreName+".rgw.")
		})},
	}
	for _, check := range checks {
		if check.configured != "" && !check.found {
			return fmt.Sprintf("adopted %s %s not found", check.kind, check.label)
		}
	}
	return ""
}

func renameConflict(state cephState, adopt v1alpha1.AdoptSpec) string {
	fs, found := findFilesystem(state, adopt.Pools.Filesystem)
	bedrockFilesystem := slices.ContainsFunc(state.Filesystems, func(other cephFilesystemState) bool { return other.Name == filesystemName })
	if found && fs.Name != filesystemName && bedrockFilesystem {
		return fmt.Sprintf("adopted filesystem %s target exists", filesystemName)
	}
	moves := slices.Concat(
		blockMoves(state, adopt.Pools.Block),
		filesystemMoves(state, adopt.Pools.Filesystem),
		objectStorePoolMoves(state, adopt.Pools.ObjectStore),
	)
	for _, move := range moves {
		if poolExists(state, move.to) {
			return fmt.Sprintf("adopted pool %s target exists", move.to)
		}
	}
	return ""
}

func adoptedPool(state cephState, adopt v1alpha1.AdoptSpec, name string) bool {
	names := []string{adopt.Pools.Block, blockPoolName, rgwRootPool}
	if fs, found := findFilesystem(state, adopt.Pools.Filesystem); found {
		names = append(names, fs.MetadataPool)
		names = append(names, fs.DataPools...)
	}
	return slices.Contains(names, name) ||
		strings.HasPrefix(name, adopt.Pools.ObjectStore+".rgw.") ||
		strings.HasPrefix(name, objectStoreName+".rgw.")
}

func cephArgs(args ...string) []string {
	return slices.Concat([]string{"ceph"}, args, []string{"--connect-timeout=10", rookConfFlag})
}

func radosgwArgs(args ...string) []string {
	return slices.Concat([]string{"radosgw-admin"}, args, []string{rookConfFlag})
}

func cephStep(args ...string) renameCommand {
	return renameCommand{Name: "ceph " + strings.Join(args, " "), Args: cephArgs(args...)}
}

func radosgwStep(args ...string) renameCommand {
	return renameCommand{Name: "radosgw-admin " + strings.Join(args, " "), Args: radosgwArgs(args...)}
}

func zonePoolRewrite(old string) renameCommand {
	return renameCommand{
		Name: "radosgw-admin zone set --rgw-zone=" + objectStoreName,
		Args: []string{"sh", "-c", zonePoolScript, "zone-rewrite", escapedDots(old), objectStoreName, rookConfFlag},
	}
}

func periodCommit() renameCommand {
	return renameCommand{
		Name: "radosgw-admin period update --commit --rgw-realm=" + objectStoreName,
		Args: []string{"sh", "-c", periodCommitScript, "period-commit", objectStoreName, rookConfFlag},
	}
}

func zoneStalePoolsCommand(old, zone string) []string {
	return []string{"sh", "-c", zoneStalePoolsScript, "zone-pools", escapedDots(old), zone, rookConfFlag}
}

func escapedDots(name string) string {
	return strings.ReplaceAll(name, ".", `\.`)
}

func quotedNames(output []byte) []string {
	var names []string
	for _, field := range strings.Fields(string(output)) {
		names = append(names, strings.Trim(field, `"`))
	}
	return names
}

func readJSON(ctx context.Context, run runFunc, target any, command []string) error {
	out, err := run(ctx, command)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, target); err != nil {
		return fmt.Errorf("parse the output of %s: %w", strings.Join(command[:2], " "), err)
	}
	return nil
}

func readCephState(ctx context.Context, run runFunc, adopt v1alpha1.AdoptSpec) (cephState, error) {
	state, err := readCephHealth(ctx, run)
	if err != nil {
		return cephState{}, err
	}
	if reason, _ := healthProblem(state, adopt); reason != "" {
		return state, nil
	}
	readers := []func(context.Context, runFunc, v1alpha1.AdoptSpec, cephState) (cephState, error){readPools, readFilesystems, readObjectStore}
	for _, read := range readers {
		next, err := read(ctx, run, adopt, state)
		if err != nil {
			return cephState{}, err
		}
		state = next
	}
	return state, nil
}

func readCephHealth(ctx context.Context, run runFunc) (cephState, error) {
	var quorum struct {
		Names []string `json:"quorum_names"`
	}
	var osds struct {
		Up int `json:"num_up_osds"`
	}
	if err := readJSON(ctx, run, &quorum, cephArgs("quorum_status", "-f", "json")); err != nil {
		return cephState{}, err
	}
	if err := readJSON(ctx, run, &osds, cephArgs("osd", "stat", "-f", "json")); err != nil {
		return cephState{}, err
	}
	return cephState{Quorum: quorum.Names, UpOSDs: osds.Up}, nil
}

func readPools(ctx context.Context, run runFunc, _ v1alpha1.AdoptSpec, state cephState) (cephState, error) {
	if err := readJSON(ctx, run, &state.Pools, cephArgs("osd", "pool", "ls", "detail", "-f", "json")); err != nil {
		return cephState{}, err
	}
	return state, nil
}

func readFilesystems(ctx context.Context, run runFunc, adopt v1alpha1.AdoptSpec, state cephState) (cephState, error) {
	if err := readJSON(ctx, run, &state.Filesystems, cephArgs("fs", "ls", "-f", "json")); err != nil {
		return cephState{}, err
	}
	for i, fs := range state.Filesystems {
		if fs.Name != adopt.Pools.Filesystem && fs.Name != filesystemName {
			continue
		}
		joinable, refuse, err := readFilesystemFlags(ctx, run, fs.Name)
		if err != nil {
			return cephState{}, err
		}
		state.Filesystems[i].Joinable, state.Filesystems[i].RefuseClientSession = joinable, refuse
	}
	return state, nil
}

func readFilesystemFlags(ctx context.Context, run runFunc, name string) (bool, bool, error) {
	var detail struct {
		MDSMap struct {
			Flags struct {
				Joinable            *bool `json:"joinable"`
				RefuseClientSession *bool `json:"refuse_client_session"`
			} `json:"flags_state"`
		} `json:"mdsmap"`
	}
	if err := readJSON(ctx, run, &detail, cephArgs("fs", "get", name, "-f", "json")); err != nil {
		return false, false, err
	}
	flags := detail.MDSMap.Flags
	if flags.Joinable == nil || flags.RefuseClientSession == nil {
		return false, false, fmt.Errorf("ceph fs get %s has no joinable or refuse_client_session flag", name)
	}
	return *flags.Joinable, *flags.RefuseClientSession, nil
}

func readObjectStore(ctx context.Context, run runFunc, adopt v1alpha1.AdoptSpec, state cephState) (cephState, error) {
	if adopt.Pools.ObjectStore == "" {
		return state, nil
	}
	var realms struct {
		Realms []string `json:"realms"`
	}
	var zoneGroups struct {
		ZoneGroups []string `json:"zonegroups"`
	}
	var zones struct {
		Zones []string `json:"zones"`
	}
	if err := readJSON(ctx, run, &realms, radosgwArgs("realm", "list")); err != nil {
		return cephState{}, err
	}
	if err := readJSON(ctx, run, &zoneGroups, radosgwArgs("zonegroup", "list")); err != nil {
		return cephState{}, err
	}
	if err := readJSON(ctx, run, &zones, radosgwArgs("zone", "list")); err != nil {
		return cephState{}, err
	}
	state.Realms, state.ZoneGroups, state.Zones = realms.Realms, zoneGroups.ZoneGroups, zones.Zones
	zone := zoneToRead(zones.Zones, adopt.Pools.ObjectStore)
	if zone == "" {
		return state, nil
	}
	out, err := run(ctx, zoneStalePoolsCommand(adopt.Pools.ObjectStore, zone))
	if err != nil {
		return cephState{}, fmt.Errorf("%w: %w", errZonePools, err)
	}
	state.ZoneStalePools = quotedNames(out)
	return state, nil
}

func zoneToRead(zones []string, old string) string {
	for _, name := range []string{objectStoreName, old} {
		if slices.Contains(zones, name) {
			return name
		}
	}
	return ""
}

func renamePlan(state cephState, adopt v1alpha1.AdoptSpec) []renameCommand {
	return slices.Concat(
		blockPlan(state, adopt.Pools.Block),
		filesystemPlan(state, adopt.Pools.Filesystem),
		objectStorePlan(state, adopt.Pools.ObjectStore),
	)
}

func poolRename(move poolMove) renameCommand {
	return cephStep("osd", "pool", "rename", move.from, move.to)
}

func poolExists(state cephState, name string) bool {
	return slices.ContainsFunc(state.Pools, func(pool cephPoolState) bool { return pool.Name == name })
}

func poolRenames(moves []poolMove) []renameCommand {
	renames := make([]renameCommand, 0, len(moves))
	for _, move := range moves {
		renames = append(renames, poolRename(move))
	}
	return renames
}

func blockMoves(state cephState, old string) []poolMove {
	if old == "" || old == blockPoolName || !poolExists(state, old) {
		return nil
	}
	return []poolMove{{old, blockPoolName}}
}

func blockPlan(state cephState, old string) []renameCommand {
	return poolRenames(blockMoves(state, old))
}

func findFilesystem(state cephState, old string) (cephFilesystemState, bool) {
	for _, name := range []string{old, filesystemName} {
		if i := slices.IndexFunc(state.Filesystems, func(fs cephFilesystemState) bool { return fs.Name == name }); i >= 0 {
			return state.Filesystems[i], true
		}
	}
	return cephFilesystemState{}, false
}

func filesystemPlan(state cephState, old string) []renameCommand {
	fs, found := findFilesystem(state, old)
	if old == "" || !found {
		return nil
	}
	renamed := fs.Name != filesystemName
	var plan []renameCommand
	if renamed {
		plan = append(plan,
			cephStep("fs", "fail", fs.Name),
			cephStep("fs", "set", fs.Name, "refuse_client_session", "true"),
			cephStep("fs", "rename", fs.Name, filesystemName, "--yes-i-really-mean-it"),
		)
	}
	plan = append(plan, poolRenames(filesystemPoolMoves(fs))...)
	if renamed || !fs.Joinable {
		plan = append(plan, cephStep("fs", "set", filesystemName, "joinable", "true"))
	}
	if renamed || fs.RefuseClientSession {
		plan = append(plan, cephStep("fs", "set", filesystemName, "refuse_client_session", "false"))
	}
	return plan
}

func filesystemMoves(state cephState, old string) []poolMove {
	fs, found := findFilesystem(state, old)
	if old == "" || !found {
		return nil
	}
	return filesystemPoolMoves(fs)
}

func filesystemPoolMoves(fs cephFilesystemState) []poolMove {
	moves := []poolMove{{fs.MetadataPool, filesystemName + "-metadata"}}
	for i, pool := range fs.DataPools {
		moves = append(moves, poolMove{pool, fmt.Sprintf("%s-data%d", filesystemName, i)})
	}
	return slices.DeleteFunc(moves, func(move poolMove) bool { return move.from == move.to })
}

func objectStorePlan(state cephState, old string) []renameCommand {
	if old == "" || old == objectStoreName {
		return nil
	}
	realm := slices.Contains(state.Realms, old)
	zoneGroup := slices.Contains(state.ZoneGroups, old)
	zone := slices.Contains(state.Zones, old)
	rewrite := len(state.ZoneStalePools) > 0
	var plan []renameCommand
	if realm {
		plan = append(plan, radosgwStep("realm", "rename", "--rgw-realm="+old, "--realm-new-name="+objectStoreName))
	}
	if zoneGroup {
		plan = append(plan, radosgwStep("zonegroup", "rename", "--rgw-zonegroup="+old, "--zonegroup-new-name="+objectStoreName))
	}
	if zone {
		plan = append(plan, radosgwStep("zone", "rename", "--rgw-zone="+old, "--zone-new-name="+objectStoreName, "--rgw-zonegroup="+objectStoreName))
	}
	if rewrite {
		plan = append(plan, zonePoolRewrite(old))
	}
	plan = append(plan, poolRenames(objectStorePoolMoves(state, old))...)
	if len(plan) > 0 {
		plan = append(plan, periodCommit())
	}
	return plan
}

func objectStorePoolMoves(state cephState, old string) []poolMove {
	if old == "" || old == objectStoreName {
		return nil
	}
	prefix := old + ".rgw."
	var moves []poolMove
	for _, pool := range state.Pools {
		if suffix, found := strings.CutPrefix(pool.Name, prefix); found {
			moves = append(moves, poolMove{pool.Name, objectStoreName + ".rgw." + suffix})
		}
	}
	slices.SortFunc(moves, func(a, b poolMove) int { return cmpString(a.from, b.from) })
	return moves
}
