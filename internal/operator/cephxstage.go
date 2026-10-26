package operator

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/cephx"
)

const (
	daemonKeyGeneration  = int64(2)
	csiKeyGeneration     = int64(3)
	rbdMirrorGeneration  = int64(2)
	keyRotationPolicy    = "KeyGeneration"
	keepPriorKeyCountMax = int64(1)
)

type LiveCephx struct {
	Exists          bool
	Restricted      bool
	Aes256kKeys     bool
	Rotated         bool
	RotationProblem string
}

type cephxPlan struct {
	Cephx        map[string]any
	AuthWarnings map[string]any
	Conditions   []metav1.Condition
}

func planCephx(in AddonInput, adoptCephx map[string]any) cephxPlan {
	cipher := in.Settings[cephxCipherKey]
	if cipher != cephx.Aes256k {
		return withAdoptedCephx(aesPlan(), adoptCephx)
	}
	_, adoptRestricts := adoptCephx["allowedCiphers"]
	blocking := cephxKernelProblems(cipher, hostsWithKernel(in.Hosts))
	if len(blocking) > 0 && !in.CephxLive.Aes256kKeys && !adoptRestricts {
		plan := aesPlan()
		plan.Conditions = []metav1.Condition{restrictedCondition(metav1.ConditionFalse, "KernelTooOld", strings.Join(blocking, "; "))}
		return withAdoptedCephx(plan, adoptCephx)
	}
	if restrictsCiphers(in, adoptCephx) {
		return withAdoptedCephx(restrictedPlan(), adoptCephx)
	}
	return withAdoptedCephx(rotatingPlan(in.CephxLive, blocking), adoptCephx)
}

func restrictsCiphers(in AddonInput, adoptCephx map[string]any) bool {
	live := in.CephxLive
	_, adoptRestricts := adoptCephx["allowedCiphers"]
	newCluster := !live.Exists && in.Cluster.Spec.Storage.Adopt == nil
	return live.Restricted || live.Rotated || adoptRestricts || newCluster
}

func hostsWithKernel(hosts []v1alpha1.Host) []v1alpha1.Host {
	return slices.DeleteFunc(slices.Clone(hosts), func(host v1alpha1.Host) bool {
		return host.Status.Inventory.Firmware.Kernel == ""
	})
}

func withAdoptedCephx(plan cephxPlan, adoptCephx map[string]any) cephxPlan {
	if adoptCephx != nil {
		plan.Cephx = mergeMaps(plan.Cephx, adoptCephx)
	}
	return plan
}

func aesPlan() cephxPlan {
	return cephxPlan{
		Cephx:        map[string]any{"csi": map[string]any{"keyType": cephx.Aes}},
		AuthWarnings: authWarnings("mute"),
	}
}

func rotatingCephx() map[string]any {
	return map[string]any{
		"daemon":        map[string]any{"keyRotationPolicy": keyRotationPolicy, "keyGeneration": daemonKeyGeneration, "keyType": cephx.Aes256k},
		"csi":           map[string]any{"keyRotationPolicy": keyRotationPolicy, "keyGeneration": csiKeyGeneration, "keepPriorKeyCountMax": keepPriorKeyCountMax, "keyType": cephx.Aes256k},
		"rbdMirrorPeer": map[string]any{"keyRotationPolicy": keyRotationPolicy, "keyGeneration": rbdMirrorGeneration, "keyType": cephx.Aes256k},
	}
}

func rotatingPlan(live LiveCephx, blocking []string) cephxPlan {
	waiting := "every cephx key must rotate to aes256k before the ciphers are restricted"
	if live.RotationProblem != "" {
		waiting += ": " + live.RotationProblem
	}
	return cephxPlan{
		Cephx:        rotatingCephx(),
		AuthWarnings: authWarnings("mute"),
		Conditions:   []metav1.Condition{restrictedCondition(metav1.ConditionFalse, "RotatingKeys", strings.Join(append([]string{waiting}, blocking...), "; "))},
	}
}

func defaultCephx() map[string]any {
	restricted := rotatingCephx()
	delete(restricted["daemon"].(map[string]any), "keyType")
	restricted["allowedCiphers"] = []any{cephx.Aes256k}
	return restricted
}

func restrictedPlan() cephxPlan {
	return cephxPlan{
		Cephx:        defaultCephx(),
		AuthWarnings: authWarnings("unmute"),
		Conditions:   []metav1.Condition{restrictedCondition(metav1.ConditionTrue, "Restricted", "only aes256k cephx keys are allowed")},
	}
}

func restrictedCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: v1alpha1.ConditionStorageCephxRestricted, Status: status, Reason: reason, Message: message}
}

func authWarnings(policy string) map[string]any {
	warnings := map[string]any{}
	for _, check := range []string{"AUTH_EMERGENCY_CIPHERS_SET", "AUTH_INSECURE_CLIENT_KEY_TYPE", "AUTH_INSECURE_KEYS_ALLOWED", "AUTH_INSECURE_KEYS_CREATABLE"} {
		warnings[check] = map[string]any{"policy": policy}
	}
	return warnings
}

func replicaWarnings(replicas int) map[string]any {
	policy := "unmute"
	if replicas == 1 {
		policy = "mute"
	}
	return map[string]any{
		"POOL_NO_REDUNDANCY": map[string]any{"policy": policy},
		"TOO_FEW_OSDS":       map[string]any{"policy": policy},
	}
}

func healthWarnings(plan cephxPlan, replicas int) map[string]any {
	warnings := maps.Clone(plan.AuthWarnings)
	maps.Copy(warnings, replicaWarnings(replicas))
	return warnings
}

func mergeMaps(base, override map[string]any) map[string]any {
	merged := maps.Clone(base)
	for key, value := range override {
		overrideMap, overrideIsMap := value.(map[string]any)
		baseMap, baseIsMap := merged[key].(map[string]any)
		if overrideIsMap && baseIsMap {
			merged[key] = mergeMaps(baseMap, overrideMap)
			continue
		}
		merged[key] = value
	}
	return merged
}

func readLiveCephx(ctx context.Context, c client.Client, exec execFunc, cipher string) (LiveCephx, error) {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cephClusterGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: storageNamespace, Name: storageNamespace}, cluster)
	if errors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return LiveCephx{}, nil
	}
	if err != nil {
		return LiveCephx{}, err
	}
	spec, _, _ := unstructured.NestedMap(cluster.Object, "spec", "security", "cephx")
	csi, _ := spec["csi"].(map[string]any)
	allowed, _ := spec["allowedCiphers"].([]any)
	live := LiveCephx{Exists: true, Restricted: slices.Contains(allowed, any(cephx.Aes256k)), Aes256kKeys: csi["keyType"] == cephx.Aes256k}
	if live.Restricted || cipher != cephx.Aes256k {
		return live, nil
	}
	status, _, _ := unstructured.NestedMap(cluster.Object, "status", "cephx")
	if !statusProvesRotation(status) {
		live.RotationProblem = "CephCluster status.cephx does not show every key rotated to aes256k yet"
		return live, nil
	}
	live.Rotated, live.RotationProblem = keysRotated(ctx, c, exec)
	return live, nil
}

func keysRotated(ctx context.Context, c client.Client, exec execFunc) (bool, string) {
	if exec == nil {
		return false, "no way to run ceph auth dump-keys"
	}
	pod, err := rookOperatorPod(ctx, c)
	if err != nil {
		return false, safeMessage(err.Error())
	}
	out, err := operatorRun(exec, pod)(ctx, cephArgs("auth", "dump-keys", "--format=json"))
	if err != nil {
		return false, "cannot read ceph auth dump-keys: " + safeMessage(err.Error())
	}
	if !onlyAes256kKeys(out) {
		return false, "ceph auth dump-keys still lists a key that is not aes256k"
	}
	return true, ""
}

func statusProvesRotation(status map[string]any) bool {
	for _, core := range []string{"mon", "mgr", "osd", "csi"} {
		if _, present := status[core]; !present {
			return false
		}
	}
	for name, raw := range status {
		entry, _ := raw.(map[string]any)
		if keyGeneration(entry["keyGeneration"]) < targetGeneration(name) {
			return false
		}
		if keyType, typed := entry["keyType"]; typed && keyType != cephx.Aes256k {
			return false
		}
	}
	return true
}

func targetGeneration(component string) int64 {
	if component == "csi" {
		return csiKeyGeneration
	}
	return daemonKeyGeneration
}

func keyGeneration(value any) int64 {
	switch number := value.(type) {
	case int64:
		return number
	case float64:
		return int64(number)
	case int:
		return int64(number)
	}
	return -1
}

func onlyAes256kKeys(out []byte) bool {
	var dump any
	if err := json.Unmarshal(out, &dump); err != nil {
		return false
	}
	types := keyTypeStrings(dump)
	return slices.Contains(types, cephx.Aes256k) && !slices.Contains(types, cephx.Aes)
}

func keyTypeStrings(node any) []string {
	switch value := node.(type) {
	case string:
		return []string{value}
	case map[string]any:
		var found []string
		for _, child := range value {
			found = append(found, keyTypeStrings(child)...)
		}
		return found
	case []any:
		var found []string
		for _, child := range value {
			found = append(found, keyTypeStrings(child)...)
		}
		return found
	}
	return nil
}
