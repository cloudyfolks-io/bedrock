package operator

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const adoptFSID = "2f3b1c1e-8c5e-4a54-9f41-0c2f6d7a9b10"

var adoptReasons = []string{
	"",
	v1alpha1.ReasonWaitingForIdentity,
	v1alpha1.ReasonWaitingForHosts,
	v1alpha1.ReasonWaitingForQuorum,
	v1alpha1.ReasonWaitingForOSDs,
	v1alpha1.ReasonRenaming,
	v1alpha1.ReasonPoolSizeTooSmall,
	v1alpha1.ReasonRenameFailed,
}

func adoptSpec() *v1alpha1.AdoptSpec {
	return &v1alpha1.AdoptSpec{
		FSID: adoptFSID,
		Monitors: []v1alpha1.AdoptMonitor{
			{ID: "a", Node: "node-1", Address: "10.0.10.11"},
			{ID: "b", Node: "node-2", Address: "10.0.10.12"},
			{ID: "c", Node: "node-3", Address: "10.0.10.13"},
		},
		Hosts: []v1alpha1.AdoptHost{
			{Node: "node-1", Devices: []string{"/dev/disk/by-id/wwn-0x1", "/dev/disk/by-id/wwn-0x2"}},
			{Node: "node-2", Devices: []string{"/dev/disk/by-id/wwn-0x3"}},
			{Node: "node-3", Devices: []string{"/dev/disk/by-id/wwn-0x4"}},
		},
		Pools: v1alpha1.AdoptPools{Block: "storage-block", Filesystem: "filesystem-data", ObjectStore: "objectstore"},
		Cephx: &runtime.RawExtension{Raw: []byte(`{"daemon":{"keyType":"aes256k","keyGeneration":2}}`)},
	}
}

func adoptHosts() []v1alpha1.Host {
	return []v1alpha1.Host{
		osdHost("node-3", "/dev/disk/by-id/wwn-0x4"),
		osdHost("node-1", "/dev/disk/by-id/wwn-0x1", "/dev/disk/by-id/wwn-0x2"),
		osdHost("node-2", "/dev/disk/by-id/wwn-0x3"),
	}
}

func monSecret(fsid string) *corev1.Secret {
	return &corev1.Secret{Data: map[string][]byte{"fsid": []byte(fsid)}}
}

func adoptInput(phase string) AddonInput {
	in := storageInput(adoptHosts(), "3")
	in.Cluster.Spec.Storage.Adopt = adoptSpec()
	in.CephMonSecret = monSecret(adoptFSID)
	if phase == "" {
		return in
	}
	status := metav1.ConditionFalse
	if phase == v1alpha1.ReasonAdopted {
		status = metav1.ConditionTrue
	}
	in.Cluster.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionStorageAdopted, Status: status, Reason: phase}}
	return in
}

func renderedKinds(objects []*unstructured.Unstructured) []string {
	kinds := make([]string, 0, len(objects))
	for _, obj := range objects {
		kinds = append(kinds, obj.GetKind())
	}
	return kinds
}

func hasKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if name == key || hasKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasKey(child, key) {
				return true
			}
		}
	}
	return false
}

func TestAdoptPhase(t *testing.T) {
	withCondition := func(adopt *v1alpha1.AdoptSpec, reason string) v1alpha1.Cluster {
		cluster := v1alpha1.Cluster{}
		cluster.Spec.Storage.Adopt = adopt
		cluster.Status.Conditions = []metav1.Condition{
			{Type: v1alpha1.ConditionStorageReady, Status: metav1.ConditionFalse, Reason: "Other"},
			{Type: v1alpha1.ConditionStorageAdopted, Status: metav1.ConditionFalse, Reason: reason},
		}
		return cluster
	}
	noCondition := v1alpha1.Cluster{}
	noCondition.Spec.Storage.Adopt = adoptSpec()
	cases := []struct {
		name    string
		cluster v1alpha1.Cluster
		want    string
	}{
		{"no adopt spec", withCondition(nil, v1alpha1.ReasonRenaming), ""},
		{"no condition", noCondition, ""},
		{"waiting", withCondition(adoptSpec(), v1alpha1.ReasonWaitingForOSDs), v1alpha1.ReasonWaitingForOSDs},
		{"adopted", withCondition(adoptSpec(), v1alpha1.ReasonAdopted), v1alpha1.ReasonAdopted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adoptPhase(tc.cluster); got != tc.want {
				t.Fatalf("phase %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderStorageAdoptGoldenCephCluster(t *testing.T) {
	out, err := RenderStorage(adoptInput(v1alpha1.ReasonAdopted))
	if err != nil {
		t.Fatal(err)
	}
	cluster := findObject(out.Objects, "CephCluster", "rook-ceph")
	if cluster == nil || cluster.GetNamespace() != "rook-ceph" || cluster.GetAPIVersion() != "ceph.rook.io/v1" {
		t.Fatalf("objects %+v", out.Objects)
	}
	want := map[string]any{
		"cephVersion":     map[string]any{"image": "quay.io/ceph/ceph:v20.2.4"},
		"dataDirHostPath": "/var/lib/rook",
		"network":         map[string]any{"provider": "host"},
		"mon":             map[string]any{"count": int64(3), "allowMultiplePerNode": false},
		"mgr":             map[string]any{"count": int64(2), "allowMultiplePerNode": false},
		"dashboard":       map[string]any{"enabled": false},
		"crashCollector":  map[string]any{"disable": true},
		"security":        map[string]any{"cephx": map[string]any{"daemon": map[string]any{"keyType": "aes256k", "keyGeneration": int64(2)}}},
		"healthCheck": map[string]any{"muteHealthWarning": map[string]any{
			"AUTH_EMERGENCY_CIPHERS_SET":    map[string]any{"policy": "mute"},
			"AUTH_INSECURE_CLIENT_KEY_TYPE": map[string]any{"policy": "mute"},
			"AUTH_INSECURE_KEYS_ALLOWED":    map[string]any{"policy": "mute"},
			"AUTH_INSECURE_KEYS_CREATABLE":  map[string]any{"policy": "mute"},
			"POOL_NO_REDUNDANCY":            map[string]any{"policy": "unmute"},
			"TOO_FEW_OSDS":                  map[string]any{"policy": "unmute"},
		}},
		"storage": map[string]any{"useAllNodes": false, "useAllDevices": false, "nodes": []any{
			map[string]any{"name": "node-1", "devices": []any{map[string]any{"name": "/dev/disk/by-id/wwn-0x1"}, map[string]any{"name": "/dev/disk/by-id/wwn-0x2"}}},
			map[string]any{"name": "node-2", "devices": []any{map[string]any{"name": "/dev/disk/by-id/wwn-0x3"}}},
			map[string]any{"name": "node-3", "devices": []any{map[string]any{"name": "/dev/disk/by-id/wwn-0x4"}}},
		}},
		"priorityClassNames": map[string]any{"mon": "system-node-critical", "osd": "system-node-critical", "mgr": "system-cluster-critical"},
	}
	if !reflect.DeepEqual(cluster.Object["spec"], want) {
		t.Fatalf("spec %+v", cluster.Object["spec"])
	}
}

func TestRenderStorageAdoptMonitorCountFollowsTheMonitors(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.Cluster.Spec.Storage.Adopt.Monitors = in.Cluster.Spec.Storage.Adopt.Monitors[:1]
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	mon, _, _ := unstructured.NestedMap(findObject(out.Objects, "CephCluster", "rook-ceph").Object, "spec", "mon")
	if !reflect.DeepEqual(mon, map[string]any{"count": int64(1), "allowMultiplePerNode": false}) {
		t.Fatalf("mon %+v", mon)
	}
}

func TestRenderStorageAdoptWithoutCephxKeepsTheDefaultKeys(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.Cluster.Spec.Storage.Adopt.Cephx = nil
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	security, _, _ := unstructured.NestedMap(findObject(out.Objects, "CephCluster", "rook-ceph").Object, "spec", "security")
	want := map[string]any{"cephx": map[string]any{"csi": map[string]any{"keyType": "aes"}}}
	if !reflect.DeepEqual(security, want) {
		t.Fatalf("security %+v", security)
	}
}

func TestRenderStorageAdoptRejectsBrokenCephx(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.Cluster.Spec.Storage.Adopt.Cephx = &runtime.RawExtension{Raw: []byte(`[1`)}
	if _, err := RenderStorage(in); err == nil {
		t.Fatal("a broken cephx must fail")
	}
}

func TestRenderStorageAdoptRendersOnlyTheCephClusterBeforeAdopted(t *testing.T) {
	healthy := cephClusterWith("Ready", "HEALTH_OK", map[string]any{"ceph version 20.2.4": int64(4)})
	for _, reason := range adoptReasons {
		t.Run("phase "+reason, func(t *testing.T) {
			out, err := RenderStorage(adoptInput(reason))
			if err != nil {
				t.Fatal(err)
			}
			if out.SkipReason != "" || !reflect.DeepEqual(renderedKinds(out.Objects), []string{"CephCluster"}) {
				t.Fatalf("rendered %+v", out)
			}
			if len(out.Probes) != 1 || out.Probes[0].Gate(healthy).Ready {
				t.Fatalf("a healthy cluster must not make storage ready before the adoption is done: %+v", out.Probes)
			}
		})
	}
}

func TestRenderStorageAdoptAdvancesToQuorum(t *testing.T) {
	cases := map[string]bool{
		"":                                true,
		v1alpha1.ReasonWaitingForIdentity: true,
		v1alpha1.ReasonWaitingForHosts:    true,
		v1alpha1.ReasonWaitingForQuorum:   false,
		v1alpha1.ReasonWaitingForOSDs:     false,
		v1alpha1.ReasonRenaming:           false,
		v1alpha1.ReasonPoolSizeTooSmall:   false,
		v1alpha1.ReasonRenameFailed:       false,
		v1alpha1.ReasonAdopted:            false,
	}
	for reason, advances := range cases {
		t.Run("phase "+reason, func(t *testing.T) {
			out, err := RenderStorage(adoptInput(reason))
			if err != nil {
				t.Fatal(err)
			}
			if !advances {
				if len(out.Conditions) != 0 {
					t.Fatalf("conditions %+v", out.Conditions)
				}
				return
			}
			want := []metav1.Condition{{
				Type:    v1alpha1.ConditionStorageAdopted,
				Status:  metav1.ConditionFalse,
				Reason:  v1alpha1.ReasonWaitingForQuorum,
				Message: "the CephCluster is rendered and waits for every monitor to join the quorum",
			}}
			if !reflect.DeepEqual(out.Conditions, want) {
				t.Fatalf("conditions %+v", out.Conditions)
			}
		})
	}
}

func TestRenderStorageAdoptedRendersTheSameObjectsAsAFreshCluster(t *testing.T) {
	adopted, err := RenderStorage(adoptInput(v1alpha1.ReasonAdopted))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := RenderStorage(storageInput(adoptHosts(), "3"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(renderedKinds(adopted.Objects), renderedKinds(fresh.Objects)) {
		t.Fatalf("kinds %v, want %v", renderedKinds(adopted.Objects), renderedKinds(fresh.Objects))
	}
	if !reflect.DeepEqual(adopted.Objects[1:], fresh.Objects[1:]) {
		t.Fatal("pools and classes of an adopted cluster must match a fresh cluster")
	}
	healthy := cephClusterWith("Ready", "HEALTH_OK", map[string]any{"ceph version 20.2.4": int64(4)})
	if len(adopted.Probes) != 1 || !adopted.Probes[0].Gate(healthy).Ready {
		t.Fatalf("probes %+v", adopted.Probes)
	}
	if len(adopted.Conditions) != 0 {
		t.Fatalf("conditions %+v", adopted.Conditions)
	}
}

func TestRenderStorageAdoptWaitsForIdentity(t *testing.T) {
	secrets := map[string]*corev1.Secret{
		"no secret":     nil,
		"another fsid":  monSecret("0b2f7c1d-1111-4222-8333-944455556666"),
		"no fsid":       {Data: map[string][]byte{"ceph-username": []byte("client.admin")}},
		"an empty fsid": monSecret(""),
	}
	for name, secret := range secrets {
		for _, reason := range append(adoptReasons, v1alpha1.ReasonAdopted) {
			t.Run(name+" at phase "+reason, func(t *testing.T) {
				in := adoptInput(reason)
				in.CephMonSecret = secret
				out, err := RenderStorage(in)
				if err != nil {
					t.Fatal(err)
				}
				if out.SkipReason != v1alpha1.ReasonWaitingForIdentity || len(out.Objects) != 0 || len(out.Probes) != 0 {
					t.Fatalf("rendered %+v", out)
				}
				if len(out.Conditions) != 1 || out.Conditions[0].Type != v1alpha1.ConditionStorageAdopted ||
					out.Conditions[0].Status != metav1.ConditionFalse || out.Conditions[0].Reason != v1alpha1.ReasonWaitingForIdentity {
					t.Fatalf("conditions %+v", out.Conditions)
				}
			})
		}
	}
}

func TestRenderStorageAdoptNamesTheIdentityProblem(t *testing.T) {
	in := adoptInput("")
	in.CephMonSecret = nil
	absent, _ := RenderStorage(in)
	in.CephMonSecret = monSecret("0b2f7c1d-1111-4222-8333-944455556666")
	other, _ := RenderStorage(in)
	if absent.SkipMessage != "Secret rook-ceph/rook-ceph-mon does not exist" {
		t.Fatalf("message %q", absent.SkipMessage)
	}
	if other.SkipMessage != "Secret rook-ceph/rook-ceph-mon holds another fsid than storage.adopt.fsid" {
		t.Fatalf("message %q", other.SkipMessage)
	}
	if absent.Conditions[0].Message != absent.SkipMessage || other.Conditions[0].Message != other.SkipMessage {
		t.Fatalf("condition messages %+v %+v", absent.Conditions, other.Conditions)
	}
}

func TestRenderStorageAdoptWaitsForMonitorHosts(t *testing.T) {
	noRole := osdHost("node-3", "/dev/disk/by-id/wwn-0x4")
	noRole.Spec.Roles = []string{v1alpha1.RoleControlPlane}
	cases := map[string][]v1alpha1.Host{
		"no host":             nil,
		"a host is missing":   {osdHost("node-1", "/dev/disk/by-id/wwn-0x1"), osdHost("node-2", "/dev/disk/by-id/wwn-0x3")},
		"a host has no role":  {osdHost("node-1", "/dev/disk/by-id/wwn-0x1"), osdHost("node-2", "/dev/disk/by-id/wwn-0x3"), noRole},
		"a host has no disks": {osdHost("node-1", "/dev/disk/by-id/wwn-0x1"), osdHost("node-2", "/dev/disk/by-id/wwn-0x3"), osdHost("node-3")},
	}
	for name, hosts := range cases {
		for _, reason := range append(adoptReasons, v1alpha1.ReasonAdopted) {
			t.Run(name+" at phase "+reason, func(t *testing.T) {
				in := adoptInput(reason)
				in.Hosts = hosts
				out, err := RenderStorage(in)
				if err != nil {
					t.Fatal(err)
				}
				if out.SkipReason != v1alpha1.ReasonWaitingForHosts || len(out.Objects) != 0 || len(out.Probes) != 0 {
					t.Fatalf("rendered %+v", out)
				}
				if len(out.Conditions) != 1 || out.Conditions[0].Type != v1alpha1.ConditionStorageAdopted ||
					out.Conditions[0].Status != metav1.ConditionFalse || out.Conditions[0].Reason != v1alpha1.ReasonWaitingForHosts {
					t.Fatalf("conditions %+v", out.Conditions)
				}
			})
		}
	}
}

func TestRenderStorageAdoptNamesTheMonitorHostsThatAreMissing(t *testing.T) {
	in := adoptInput("")
	in.Hosts = []v1alpha1.Host{osdHost("node-2", "/dev/disk/by-id/wwn-0x3")}
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipMessage != "monitor hosts without the ceph-osd role and devices: node-1, node-3" {
		t.Fatalf("message %q", out.SkipMessage)
	}
}

func TestRenderStorageAdoptChecksTheIdentityBeforeTheHosts(t *testing.T) {
	in := adoptInput("")
	in.CephMonSecret = nil
	in.Hosts = nil
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != v1alpha1.ReasonWaitingForIdentity {
		t.Fatalf("reason %q", out.SkipReason)
	}
}

func TestRenderStorageAdoptRendersNoPoolBeforeAdopted(t *testing.T) {
	poolKinds := []string{"CephBlockPool", "CephFilesystem", "CephObjectStore", "StorageClass", "VolumeSnapshotClass"}
	secrets := []*corev1.Secret{nil, monSecret(adoptFSID)}
	hostSets := [][]v1alpha1.Host{nil, adoptHosts()}
	for _, reason := range adoptReasons {
		for _, secret := range secrets {
			for _, hosts := range hostSets {
				in := adoptInput(reason)
				in.CephMonSecret = secret
				in.Hosts = hosts
				out, err := RenderStorage(in)
				if err != nil {
					t.Fatal(err)
				}
				for _, kind := range renderedKinds(out.Objects) {
					for _, pool := range poolKinds {
						if kind == pool {
							t.Fatalf("phase %q rendered %s", reason, kind)
						}
					}
				}
			}
		}
	}
}

func TestRenderStorageNeverRendersCleanupPolicy(t *testing.T) {
	inputs := map[string]AddonInput{
		"fresh":         storageInput([]v1alpha1.Host{osdHost("a", "/dev/sdb")}, "1"),
		"fresh three":   storageInput(adoptHosts(), "3"),
		"adopt waiting": adoptInput(v1alpha1.ReasonWaitingForQuorum),
		"adopt renamed": adoptInput(v1alpha1.ReasonAdopted),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			out, err := RenderStorage(in)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Objects) == 0 {
				t.Fatal("nothing rendered")
			}
			for _, obj := range out.Objects {
				if hasKey(obj.Object, "cleanupPolicy") {
					t.Fatalf("%s %s has a cleanupPolicy", obj.GetKind(), obj.GetName())
				}
			}
		})
	}
}
