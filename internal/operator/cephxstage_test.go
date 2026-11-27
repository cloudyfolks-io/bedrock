package operator

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func renderedCephx(t *testing.T, in AddonInput) (map[string]any, Rendered) {
	t.Helper()
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	cephx, _, _ := unstructured.NestedMap(findObject(out.Objects, "CephCluster", "rook-ceph").Object, "spec", "security", "cephx")
	return cephx, out
}

func existingCluster(live LiveCephx) AddonInput {
	in := withCipher(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "3"), "aes256k")
	live.Exists = true
	in.CephxLive = live
	return in
}

func stageACephx() map[string]any {
	return map[string]any{
		"daemon":        map[string]any{"keyRotationPolicy": "KeyGeneration", "keyGeneration": int64(2), "keyType": "aes256k"},
		"csi":           map[string]any{"keyRotationPolicy": "KeyGeneration", "keyGeneration": int64(3), "keepPriorKeyCountMax": int64(1), "keyType": "aes256k"},
		"rbdMirrorPeer": map[string]any{"keyRotationPolicy": "KeyGeneration", "keyGeneration": int64(2), "keyType": "aes256k"},
	}
}

func TestRenderStorageRotatesBeforeItRestrictsAnExistingCluster(t *testing.T) {
	cephx, out := renderedCephx(t, existingCluster(LiveCephx{}))
	if !reflect.DeepEqual(cephx, stageACephx()) {
		t.Fatalf("cephx %+v", cephx)
	}
	for check, policy := range authPolicies(t, existingCluster(LiveCephx{})) {
		if strings.HasPrefix(check, "AUTH_") && policy != "mute" {
			t.Errorf("%s is %s in stage A", check, policy)
		}
	}
	if len(out.Conditions) != 1 || out.Conditions[0].Type != v1alpha1.ConditionStorageCephxRestricted || out.Conditions[0].Status != "False" {
		t.Fatalf("conditions %+v", out.Conditions)
	}
}

func TestRenderStorageRestrictsOnceEveryKeyIsRotated(t *testing.T) {
	in := existingCluster(LiveCephx{Rotated: true})
	cephx, out := renderedCephx(t, in)
	if !reflect.DeepEqual(cephx["allowedCiphers"], []any{"aes256k"}) {
		t.Fatalf("cephx %+v", cephx)
	}
	for check, policy := range authPolicies(t, in) {
		if strings.HasPrefix(check, "AUTH_") && policy != "unmute" {
			t.Errorf("%s is %s in stage B", check, policy)
		}
	}
	if len(out.Conditions) != 1 || out.Conditions[0].Status != "True" {
		t.Fatalf("conditions %+v", out.Conditions)
	}
}

func TestRenderStorageKeepsTheRestrictionOfARestrictedCluster(t *testing.T) {
	cephx, _ := renderedCephx(t, existingCluster(LiveCephx{Restricted: true}))
	if !reflect.DeepEqual(cephx["allowedCiphers"], []any{"aes256k"}) {
		t.Fatalf("cephx %+v", cephx)
	}
}

func TestRenderStorageStaysInStageAWithTheReasonThatBlocksTheProof(t *testing.T) {
	_, out := renderedCephx(t, existingCluster(LiveCephx{RotationProblem: "cannot read ceph auth dump-keys"}))
	if len(out.Conditions) != 1 || !strings.Contains(out.Conditions[0].Message, "cannot read ceph auth dump-keys") {
		t.Fatalf("conditions %+v", out.Conditions)
	}
}

func TestRenderStorageAdoptionRestrictsAtOnceWhenTheOldClusterIs(t *testing.T) {
	in := withCipher(adoptInput(v1alpha1.ReasonAdopted), "aes256k")
	in.Cluster.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`{"allowedCiphers":["aes256k"],"daemon":{"keyGeneration":2},"csi":{"keyGeneration":3,"keyType":"aes256k"},"rbdMirrorPeer":{"keyGeneration":2}}`)}
	cephx, _ := renderedCephx(t, in)
	if !reflect.DeepEqual(cephx["allowedCiphers"], []any{"aes256k"}) {
		t.Fatalf("cephx %+v", cephx)
	}
}

func TestRenderStorageAdoptionOfARotatedClusterWithoutRestrictionStartsInStageA(t *testing.T) {
	in := withCipher(adoptInput(v1alpha1.ReasonAdopted), "aes256k")
	in.Cluster.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`{"daemon":{"keyGeneration":2},"csi":{"keyGeneration":3,"keyType":"aes256k"},"rbdMirrorPeer":{"keyGeneration":2}}`)}
	cephx, _ := renderedCephx(t, in)
	if _, restricted := cephx["allowedCiphers"]; restricted {
		t.Fatalf("cephx %+v", cephx)
	}
}

func TestRenderStorageAdoptionOfAnAesClusterKeepsAes(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.Cluster.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`{"csi":{"keyGeneration":1,"keyType":"aes"}}`)}
	cephx, _ := renderedCephx(t, in)
	want := map[string]any{"csi": map[string]any{"keyType": "aes", "keyGeneration": int64(1)}}
	if !reflect.DeepEqual(cephx, want) {
		t.Fatalf("cephx %+v", cephx)
	}
}

func TestRenderStorageKeepsAesWhileAHostKernelIsTooOld(t *testing.T) {
	in := existingCluster(LiveCephx{})
	old := osdHost("old", "/dev/sdc")
	old.Status.Inventory.Firmware.Kernel = "6.8.0-45-generic"
	in.Hosts = append(in.Hosts, old)
	cephx, out := renderedCephx(t, in)
	if !reflect.DeepEqual(cephx, map[string]any{"csi": map[string]any{"keyType": "aes"}}) {
		t.Fatalf("cephx %+v", cephx)
	}
	if len(out.Conditions) != 1 || !strings.Contains(out.Conditions[0].Message, "kernel 7.0") || !strings.Contains(out.Conditions[0].Message, "old") {
		t.Fatalf("conditions %+v", out.Conditions)
	}
}

func TestRenderStorageIgnoresHostsWithoutInventoryYet(t *testing.T) {
	in := withCipher(storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "3"), "aes256k")
	cephx, _ := renderedCephx(t, in)
	if _, restricted := cephx["allowedCiphers"]; !restricted {
		t.Fatalf("cephx %+v", cephx)
	}
}

func TestCephxStatusProvesRotationOnlyAtTheTargetGenerations(t *testing.T) {
	rotated := map[string]any{
		"mon": map[string]any{"keyGeneration": int64(2)}, "mgr": map[string]any{"keyGeneration": int64(2)}, "osd": map[string]any{"keyGeneration": int64(2)},
		"admin": map[string]any{"keyGeneration": int64(2)}, "csi": map[string]any{"keyGeneration": int64(3), "keyType": "aes256k"},
		"rbdMirrorPeer": map[string]any{"keyGeneration": int64(2), "keyType": "aes256k"},
	}
	if !statusProvesRotation(rotated) {
		t.Fatal("a rotated status must prove it")
	}
	behind := map[string]any{}
	for key, value := range rotated {
		behind[key] = value
	}
	behind["osd"] = map[string]any{"keyGeneration": int64(1)}
	if statusProvesRotation(behind) {
		t.Fatal("an osd on an old generation must not prove it")
	}
	aes := map[string]any{}
	for key, value := range rotated {
		aes[key] = value
	}
	aes["csi"] = map[string]any{"keyGeneration": int64(3), "keyType": "aes"}
	if statusProvesRotation(aes) {
		t.Fatal("an aes csi key must not prove it")
	}
	if statusProvesRotation(nil) || statusProvesRotation(map[string]any{"mon": map[string]any{"keyGeneration": int64(2)}}) {
		t.Fatal("a status without the core daemons must not prove it")
	}
}

func TestAuthDumpKeysMustHoldNoAesKey(t *testing.T) {
	if !onlyAes256kKeys([]byte(`{"mon.":{"key":"AQ","type":"aes256k"},"client.admin":{"key":"AQ","type":"aes256k"}}`)) {
		t.Fatal("only aes256k keys must pass")
	}
	if onlyAes256kKeys([]byte(`{"mon.":{"key":"AQ","type":"aes256k"},"client.csi":{"key":"AQ","type":"aes"}}`)) {
		t.Fatal("an aes key must fail")
	}
	if onlyAes256kKeys([]byte(`{"mon.":{"key":"AQ"}}`)) || onlyAes256kKeys([]byte(`not json`)) {
		t.Fatal("output that proves nothing must fail")
	}
}

const liveCephxOfIrCentral1 = `{"allowedCiphers":["aes256k"],"daemon":{"keyRotationPolicy":"KeyGeneration","keyGeneration":2},"csi":{"keyRotationPolicy":"KeyGeneration","keyGeneration":3,"keepPriorKeyCountMax":1,"keyType":"aes256k"},"rbdMirrorPeer":{"keyRotationPolicy":"KeyGeneration","keyGeneration":2,"keyType":"aes256k"}}`

func adoptedLiveInput() AddonInput {
	in := withCipher(adoptInput(v1alpha1.ReasonAdopted), "aes256k")
	in.Cluster.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(liveCephxOfIrCentral1)}
	return in
}

func TestRenderStorageAdoptedLiveCephxRendersTheSecurityAndHealthBlocksExactly(t *testing.T) {
	out, err := RenderStorage(adoptedLiveInput())
	if err != nil {
		t.Fatal(err)
	}
	spec := findObject(out.Objects, "CephCluster", "rook-ceph").Object["spec"].(map[string]any)
	wantSecurity := map[string]any{"cephx": map[string]any{
		"allowedCiphers": []any{"aes256k"},
		"daemon":         map[string]any{"keyRotationPolicy": "KeyGeneration", "keyGeneration": int64(2)},
		"csi":            map[string]any{"keyRotationPolicy": "KeyGeneration", "keyGeneration": int64(3), "keepPriorKeyCountMax": int64(1), "keyType": "aes256k"},
		"rbdMirrorPeer":  map[string]any{"keyRotationPolicy": "KeyGeneration", "keyGeneration": int64(2), "keyType": "aes256k"},
	}}
	if !reflect.DeepEqual(spec["security"], wantSecurity) {
		t.Fatalf("security %+v", spec["security"])
	}
	wantHealth := map[string]any{"muteHealthWarning": map[string]any{
		"AUTH_EMERGENCY_CIPHERS_SET":    map[string]any{"policy": "unmute"},
		"AUTH_INSECURE_CLIENT_KEY_TYPE": map[string]any{"policy": "unmute"},
		"AUTH_INSECURE_KEYS_ALLOWED":    map[string]any{"policy": "unmute"},
		"AUTH_INSECURE_KEYS_CREATABLE":  map[string]any{"policy": "unmute"},
		"POOL_NO_REDUNDANCY":            map[string]any{"policy": "unmute"},
		"TOO_FEW_OSDS":                  map[string]any{"policy": "unmute"},
	}}
	if !reflect.DeepEqual(spec["healthCheck"], wantHealth) {
		t.Fatalf("healthCheck %+v", spec["healthCheck"])
	}
}

func TestRenderStorageAdoptedRestrictedClusterIgnoresTheKernelGate(t *testing.T) {
	in := adoptedLiveInput()
	old := osdHost("old", "/dev/sdc")
	old.Status.Inventory.Firmware.Kernel = "6.8.0-45-generic"
	in.Hosts = append(in.Hosts, old)
	cephx, out := renderedCephx(t, in)
	if !reflect.DeepEqual(cephx["allowedCiphers"], []any{"aes256k"}) || cephx["csi"].(map[string]any)["keyType"] != "aes256k" {
		t.Fatalf("cephx %+v", cephx)
	}
	for _, condition := range out.Conditions {
		if condition.Reason == "KernelTooOld" {
			t.Fatalf("conditions %+v", out.Conditions)
		}
	}
	for check, policy := range authPolicies(t, in) {
		if strings.HasPrefix(check, "AUTH_") && policy != "unmute" {
			t.Errorf("%s is %s", check, policy)
		}
	}
}
