package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
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
		for _, reason := range adoptReasons {
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

func TestRenderStorageAdoptedIsTerminal(t *testing.T) {
	lostMonitorHost := []v1alpha1.Host{osdHost("node-1", "/dev/disk/by-id/wwn-0x1"), osdHost("node-2", "/dev/disk/by-id/wwn-0x3")}
	cases := map[string]func(*AddonInput){
		"no Secret":           func(in *AddonInput) { in.CephMonSecret = nil },
		"another fsid":        func(in *AddonInput) { in.CephMonSecret = monSecret("0b2f7c1d-1111-4222-8333-944455556666") },
		"a monitor host lost": func(in *AddonInput) { in.Hosts = lostMonitorHost },
		"both":                func(in *AddonInput) { in.CephMonSecret, in.Hosts = nil, lostMonitorHost },
	}
	fresh, err := RenderStorage(storageInput(lostMonitorHost, "3"))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := adoptInput(v1alpha1.ReasonAdopted)
			change(&in)
			out, err := RenderStorage(in)
			if err != nil {
				t.Fatal(err)
			}
			if out.SkipReason != "" || len(out.Conditions) != 0 {
				t.Fatalf("skip %q conditions %+v", out.SkipReason, out.Conditions)
			}
			if !reflect.DeepEqual(renderedKinds(out.Objects), renderedKinds(fresh.Objects)) {
				t.Fatalf("kinds %v", renderedKinds(out.Objects))
			}
		})
	}
}

func TestRenderStorageAdoptedWithoutOSDHostsRendersNothing(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.Hosts = nil
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "NoDevices" || len(out.Objects) != 0 || len(out.Conditions) != 0 {
		t.Fatalf("rendered %+v", out)
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
		for _, reason := range adoptReasons {
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
		"adopt adopted": adoptInput(v1alpha1.ReasonAdopted),
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

const (
	keyQuorum     = "ceph quorum_status -f json"
	keyOSDs       = "ceph osd stat -f json"
	keyPools      = "ceph osd pool ls detail -f json"
	keyFS         = "ceph fs ls -f json"
	keyOldFS      = "ceph fs get filesystem-data -f json"
	keyNewFS      = "ceph fs get bedrock-fs -f json"
	keyRealms     = "radosgw-admin realm list"
	keyZoneGroups = "radosgw-admin zonegroup list"
	keyZones      = "radosgw-admin zone list"
	keyOldZone    = "sh -c zone-pools objectstore objectstore"
	keyNewZone    = "sh -c zone-pools objectstore bedrock-rgw"
)

var (
	blockRename  = []string{"ceph osd pool rename storage-block bedrock-block"}
	cephFSRename = []string{
		"ceph fs fail filesystem-data",
		"ceph fs set filesystem-data refuse_client_session true",
		"ceph fs rename filesystem-data bedrock-fs --yes-i-really-mean-it",
		"ceph osd pool rename filesystem-data-metadata bedrock-fs-metadata",
		"ceph osd pool rename filesystem-data-data0 bedrock-fs-data0",
		"ceph fs set bedrock-fs joinable true",
		"ceph fs set bedrock-fs refuse_client_session false",
	}
	cephFSFlags = []string{
		"ceph fs set bedrock-fs joinable true",
		"ceph fs set bedrock-fs refuse_client_session false",
	}
	rgwIdentityRename = []string{
		"radosgw-admin realm rename --rgw-realm=objectstore --realm-new-name=bedrock-rgw",
		"radosgw-admin zonegroup rename --rgw-zonegroup=objectstore --zonegroup-new-name=bedrock-rgw",
		"radosgw-admin zone rename --rgw-zone=objectstore --zone-new-name=bedrock-rgw --rgw-zonegroup=bedrock-rgw",
	}
	rgwZoneRewrite = []string{
		"radosgw-admin zone set --rgw-zone=bedrock-rgw",
		"radosgw-admin period update --commit --rgw-realm=bedrock-rgw",
	}
	rgwPoolRenames = []string{
		"ceph osd pool rename objectstore.rgw.buckets.data bedrock-rgw.rgw.buckets.data",
		"ceph osd pool rename objectstore.rgw.buckets.index bedrock-rgw.rgw.buckets.index",
		"ceph osd pool rename objectstore.rgw.buckets.non-ec bedrock-rgw.rgw.buckets.non-ec",
		"ceph osd pool rename objectstore.rgw.control bedrock-rgw.rgw.control",
		"ceph osd pool rename objectstore.rgw.log bedrock-rgw.rgw.log",
		"ceph osd pool rename objectstore.rgw.meta bedrock-rgw.rgw.meta",
		"ceph osd pool rename objectstore.rgw.otp bedrock-rgw.rgw.otp",
	}
)

type fakeCeph struct {
	outputs   map[string]json.RawMessage
	failures  map[string]error
	reads     []string
	mutations []string
}

func commandKey(command []string) string {
	kept := make([]string, 0, len(command))
	for _, arg := range command {
		if !strings.HasPrefix(arg, "--conf=") && !strings.HasPrefix(arg, "--connect-timeout=") {
			kept = append(kept, arg)
		}
	}
	if kept[0] == "sh" && len(kept) > 3 {
		kept = slices.Delete(kept, 2, 3)
	}
	return strings.Join(kept, " ")
}

func zonePoolLines(output []byte) []byte {
	var names []string
	if err := json.Unmarshal(output, &names); err != nil {
		panic(err)
	}
	var lines []byte
	for _, name := range names {
		lines = append(lines, []byte(`"`+name+`"`+"\n")...)
	}
	return lines
}

func (f *fakeCeph) run(_ context.Context, command []string) ([]byte, error) {
	key := commandKey(command)
	if err := f.failures[key]; err != nil {
		f.mutations = append(f.mutations, key)
		return nil, err
	}
	if output, found := f.outputs[key]; found {
		f.reads = append(f.reads, key)
		if strings.HasPrefix(key, "sh -c zone-pools ") {
			return zonePoolLines(output), nil
		}
		return output, nil
	}
	f.mutations = append(f.mutations, key)
	return nil, nil
}

func cephFixture(t *testing.T, overrides ...map[string]any) *fakeCeph {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "ceph", "fresh.json"))
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &outputs); err != nil {
		t.Fatal(err)
	}
	for _, group := range overrides {
		for key, value := range group {
			if value == nil {
				delete(outputs, key)
				continue
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			outputs[key] = raw
		}
	}
	return &fakeCeph{outputs: outputs, failures: map[string]error{}}
}

func poolEntries(size int, names ...string) []map[string]any {
	pools := make([]map[string]any, 0, len(names))
	for i, name := range names {
		pools = append(pools, map[string]any{"pool_id": i + 1, "pool_name": name, "size": size})
	}
	return pools
}

func rgwPoolNames(prefix string) []string {
	var names []string
	for _, suffix := range []string{"control", "meta", "log", "buckets.index", "buckets.non-ec", "otp", "buckets.data"} {
		names = append(names, prefix+".rgw."+suffix)
	}
	return names
}

func clusterPools(block, metadata, data, rgwPrefix string) []map[string]any {
	names := slices.Concat([]string{".mgr", block, metadata, data, ".rgw.root"}, rgwPoolNames(rgwPrefix))
	return poolEntries(3, names...)
}

func filesystemEntry(name, metadata, data string) []map[string]any {
	return []map[string]any{{"name": name, "metadata_pool": metadata, "data_pools": []string{data}}}
}

func filesystemFlags(joinable, refuse bool) map[string]any {
	return map[string]any{"mdsmap": map[string]any{"flags_state": map[string]any{"joinable": joinable, "refuse_client_session": refuse}}}
}

func nameList(field string, names ...string) map[string]any {
	return map[string]any{"default_info": "id", field: names}
}

func zoneDocument(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "ceph", "zone.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func zonePoolRefs(t *testing.T, prefix string) []string {
	t.Helper()
	if prefix != "objectstore" {
		return []string{}
	}
	var names []string
	if err := json.Unmarshal(cephFixture(t).outputs[keyOldZone], &names); err != nil {
		t.Fatal(err)
	}
	return names
}

func blockDone() map[string]any {
	return map[string]any{keyPools: clusterPools("bedrock-block", "filesystem-data-metadata", "filesystem-data-data0", "objectstore")}
}

func filesystemDone() map[string]any {
	return map[string]any{
		keyPools: clusterPools("bedrock-block", "bedrock-fs-metadata", "bedrock-fs-data0", "objectstore"),
		keyFS:    filesystemEntry("bedrock-fs", "bedrock-fs-metadata", "bedrock-fs-data0"),
		keyOldFS: nil,
		keyNewFS: filesystemFlags(true, false),
	}
}

func identityRenamed(t *testing.T, zonePrefix string) map[string]any {
	return map[string]any{
		keyRealms:     nameList("realms", "bedrock-rgw"),
		keyZoneGroups: nameList("zonegroups", "bedrock-rgw"),
		keyZones:      nameList("zones", "bedrock-rgw"),
		keyOldZone:    nil,
		keyNewZone:    zonePoolRefs(t, zonePrefix),
	}
}

func allDone(t *testing.T) []map[string]any {
	done := filesystemDone()
	done[keyPools] = clusterPools("bedrock-block", "bedrock-fs-metadata", "bedrock-fs-data0", "bedrock-rgw")
	return []map[string]any{done, identityRenamed(t, "bedrock-rgw")}
}

func planNames(plan []renameCommand) []string {
	names := make([]string, 0, len(plan))
	for _, command := range plan {
		names = append(names, command.Name)
	}
	return names
}

func TestRenamePlan(t *testing.T) {
	cases := []struct {
		name      string
		overrides []map[string]any
		want      []string
	}{
		{"fresh", nil, slices.Concat(blockRename, cephFSRename, rgwIdentityRename, rgwZoneRewrite, rgwPoolRenames)},
		{"block done", []map[string]any{blockDone()}, slices.Concat(cephFSRename, rgwIdentityRename, rgwZoneRewrite, rgwPoolRenames)},
		{
			"CephFS half done",
			[]map[string]any{{
				keyPools: clusterPools("bedrock-block", "bedrock-fs-metadata", "filesystem-data-data0", "objectstore"),
				keyFS:    filesystemEntry("bedrock-fs", "bedrock-fs-metadata", "filesystem-data-data0"),
				keyOldFS: nil,
				keyNewFS: filesystemFlags(false, true),
			}},
			slices.Concat([]string{"ceph osd pool rename filesystem-data-data0 bedrock-fs-data0"}, cephFSFlags, rgwIdentityRename, rgwZoneRewrite, rgwPoolRenames),
		},
		{
			"CephFS renamed and pools renamed but flags pending",
			[]map[string]any{filesystemDone(), {keyNewFS: filesystemFlags(false, true)}, identityRenamed(t, "bedrock-rgw"), {keyPools: clusterPools("bedrock-block", "bedrock-fs-metadata", "bedrock-fs-data0", "bedrock-rgw")}},
			cephFSFlags,
		},
		{
			"RGW zone renamed but pools not",
			[]map[string]any{filesystemDone(), identityRenamed(t, "objectstore")},
			slices.Concat(rgwZoneRewrite, rgwPoolRenames),
		},
		{
			"RGW zone rewritten and some pools renamed",
			[]map[string]any{filesystemDone(), identityRenamed(t, "bedrock-rgw"), {keyPools: poolEntries(3,
				".mgr", "bedrock-block", "bedrock-fs-metadata", "bedrock-fs-data0", ".rgw.root",
				"bedrock-rgw.rgw.buckets.data", "bedrock-rgw.rgw.buckets.index",
				"objectstore.rgw.buckets.non-ec", "objectstore.rgw.control", "objectstore.rgw.log", "objectstore.rgw.meta", "objectstore.rgw.otp")}},
			rgwPoolRenames[2:],
		},
		{"all done", allDone(t), nil},
		{
			"no CephFS and no object store",
			[]map[string]any{{
				keyPools:      poolEntries(3, ".mgr", "storage-block"),
				keyFS:         []map[string]any{},
				keyOldFS:      nil,
				keyRealms:     nameList("realms"),
				keyZoneGroups: nameList("zonegroups"),
				keyZones:      nameList("zones"),
				keyOldZone:    nil,
			}},
			blockRename,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := cephFixture(t, tc.overrides...)
			state, err := readCephState(context.Background(), fake.run, *adoptSpec())
			if err != nil {
				t.Fatal(err)
			}
			got := planNames(renamePlan(state, *adoptSpec()))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("plan\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			if len(fake.mutations) != 0 {
				t.Fatalf("reading the state ran %v", fake.mutations)
			}
		})
	}
}

func TestRenamePlanLeavesAnExistingBedrockFilesystemAloneForAnEmptyName(t *testing.T) {
	adopt := adoptSpec()
	adopt.Pools.Filesystem = ""
	pending := map[string]any{
		keyFS:    filesystemEntry("bedrock-fs", "filesystem-data-metadata", "filesystem-data-data0"),
		keyOldFS: nil,
		keyNewFS: filesystemFlags(false, true),
	}
	state, err := readCephState(context.Background(), cephFixture(t, pending).run, *adopt)
	if err != nil {
		t.Fatal(err)
	}
	got := planNames(renamePlan(state, *adopt))
	want := slices.Concat(blockRename, rgwIdentityRename, rgwZoneRewrite, rgwPoolRenames)
	if !slices.Equal(got, want) {
		t.Fatalf("plan\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if conflict := renameConflict(state, *adopt); conflict != "" {
		t.Fatalf("conflict %q", conflict)
	}
}

func TestRenamePlanKeepsTheNamesThatAreAlreadyBedrockNames(t *testing.T) {
	adopt := adoptSpec()
	adopt.Pools = v1alpha1.AdoptPools{Block: "bedrock-block", Filesystem: "bedrock-fs", ObjectStore: "bedrock-rgw"}
	fake := cephFixture(t, allDone(t)...)
	state, err := readCephState(context.Background(), fake.run, *adopt)
	if err != nil {
		t.Fatal(err)
	}
	if plan := renamePlan(state, *adopt); len(plan) != 0 {
		t.Fatalf("plan %v", planNames(plan))
	}
}

func TestRenamePlanSkipsTheNamesThatAreEmpty(t *testing.T) {
	cases := []struct {
		name  string
		empty func(*v1alpha1.AdoptPools)
		want  []string
	}{
		{"block", func(pools *v1alpha1.AdoptPools) { pools.Block = "" }, slices.Concat(cephFSRename, rgwIdentityRename, rgwZoneRewrite, rgwPoolRenames)},
		{"filesystem", func(pools *v1alpha1.AdoptPools) { pools.Filesystem = "" }, slices.Concat(blockRename, rgwIdentityRename, rgwZoneRewrite, rgwPoolRenames)},
		{"object store", func(pools *v1alpha1.AdoptPools) { pools.ObjectStore = "" }, slices.Concat(blockRename, cephFSRename)},
		{"all", func(pools *v1alpha1.AdoptPools) { *pools = v1alpha1.AdoptPools{} }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adopt := adoptSpec()
			tc.empty(&adopt.Pools)
			state, err := readCephState(context.Background(), cephFixture(t).run, *adopt)
			if err != nil {
				t.Fatal(err)
			}
			got := planNames(renamePlan(state, *adopt))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("plan\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestReadCephStateReadsTheInventoryOnlyAfterTheMonitorsAndOSDsAnswer(t *testing.T) {
	cases := map[string]map[string]any{
		"a monitor is out of quorum": {keyQuorum: map[string]any{"quorum_names": []string{"a", "b"}}},
		"an OSD is down":             {keyOSDs: map[string]any{"num_up_osds": 3}},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			fake := cephFixture(t, override)
			state, err := readCephState(context.Background(), fake.run, *adoptSpec())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(fake.reads, []string{keyQuorum, keyOSDs}) || len(state.Pools) != 0 {
				t.Fatalf("reads %v, pools %v", fake.reads, state.Pools)
			}
		})
	}
}

func TestReadCephStateSkipsTheObjectStoreForAnEmptyName(t *testing.T) {
	adopt := adoptSpec()
	adopt.Pools.ObjectStore = ""
	fake := cephFixture(t)
	state, err := readCephState(context.Background(), fake.run, *adopt)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range slices.Concat(fake.reads, fake.mutations) {
		if strings.HasPrefix(key, "radosgw-admin") || strings.HasPrefix(key, "sh ") {
			t.Fatalf("an empty object store name must not run %q", key)
		}
	}
	if len(state.Realms)+len(state.ZoneGroups)+len(state.Zones)+len(state.ZoneStalePools) != 0 || len(state.Pools) == 0 {
		t.Fatalf("state %+v", state)
	}
}

func TestReadCephStateFailsLoudWithoutTheFlagsOfTheFilesystem(t *testing.T) {
	fake := cephFixture(t, map[string]any{keyOldFS: map[string]any{"mdsmap": map[string]any{"flags_state": map[string]any{"allow_snaps": true}}}})
	if _, err := readCephState(context.Background(), fake.run, *adoptSpec()); err == nil {
		t.Fatal("a filesystem without the joinable flag must fail the read")
	}
}

func executedPlan(plan []renameCommand) []string {
	keys := make([]string, 0, len(plan))
	for _, command := range plan {
		keys = append(keys, commandKey(command.Args))
	}
	return keys
}

func TestAdoptStepGates(t *testing.T) {
	cases := []struct {
		name      string
		overrides []map[string]any
		replicas  int
		reason    string
		message   string
		reads     int
	}{
		{"monitor out of quorum", []map[string]any{{keyQuorum: map[string]any{"quorum_names": []string{"a", "c"}}}}, 3, v1alpha1.ReasonWaitingForQuorum, "monitors not in quorum: b", 2},
		{"OSD down", []map[string]any{{keyOSDs: map[string]any{"num_up_osds": 3}}}, 3, v1alpha1.ReasonWaitingForOSDs, "3 of 4 OSDs are up", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := cephFixture(t, tc.overrides...)
			got := adoptStep(context.Background(), fake.run, *adoptSpec(), tc.replicas)
			if got.Type != v1alpha1.ConditionStorageAdopted || got.Status != metav1.ConditionFalse || got.Reason != tc.reason || got.Message != tc.message {
				t.Fatalf("condition %+v", got)
			}
			if len(fake.reads) != tc.reads || len(fake.mutations) != 0 {
				t.Fatalf("reads %v, mutations %v", fake.reads, fake.mutations)
			}
		})
	}
}

func TestAdoptStepRefusesPoolsLargerThanTheReplicas(t *testing.T) {
	states := map[string][]map[string]any{"fresh": nil, "renamed": allDone(t)}
	for name, overrides := range states {
		t.Run(name, func(t *testing.T) {
			fake := cephFixture(t, overrides...)
			got := adoptStep(context.Background(), fake.run, *adoptSpec(), 1)
			if got.Status != metav1.ConditionFalse || got.Reason != v1alpha1.ReasonPoolSizeTooSmall {
				t.Fatalf("condition %+v", got)
			}
			if len(fake.mutations) != 0 {
				t.Fatalf("a failed size gate ran %v", fake.mutations)
			}
			if strings.Contains(got.Message, ".mgr") || !strings.Contains(got.Message, "storage.replicas is 1") {
				t.Fatalf("message %q", got.Message)
			}
		})
	}
}

func TestAdoptStepNamesEveryAdoptedPoolThatIsTooLarge(t *testing.T) {
	fake := cephFixture(t)
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 2)
	for _, pool := range []string{"storage-block (3)", "filesystem-data-metadata (3)", "filesystem-data-data0 (3)", ".rgw.root (3)", "objectstore.rgw.buckets.data (3)"} {
		if !strings.Contains(got.Message, pool) {
			t.Fatalf("message %q lacks %s", got.Message, pool)
		}
	}
}

func TestAdoptStepAcceptsPoolsThatFitTheReplicas(t *testing.T) {
	fake := cephFixture(t, map[string]any{keyPools: poolEntries(1, ".mgr", "storage-block", "filesystem-data-metadata", "filesystem-data-data0", ".rgw.root")})
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 1)
	if got.Reason == v1alpha1.ReasonPoolSizeTooSmall {
		t.Fatalf("condition %+v", got)
	}
}

func TestAdoptStepRunsThePlanInOrder(t *testing.T) {
	fake := cephFixture(t)
	state, err := readCephState(context.Background(), cephFixture(t).run, *adoptSpec())
	if err != nil {
		t.Fatal(err)
	}
	plan := renamePlan(state, *adoptSpec())
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 3)
	if got.Status != metav1.ConditionFalse || got.Reason != v1alpha1.ReasonRenaming {
		t.Fatalf("condition %+v", got)
	}
	if !slices.Equal(fake.mutations, executedPlan(plan)) || len(plan) != 20 {
		t.Fatalf("ran %d commands:\n%s", len(fake.mutations), strings.Join(fake.mutations, "\n"))
	}
}

func TestAdoptStepStopsAtTheFirstFailureWithoutKeyLines(t *testing.T) {
	fake := cephFixture(t)
	failing := "ceph fs rename filesystem-data bedrock-fs --yes-i-really-mean-it"
	fake.failures[failing] = errors.New("command terminated with exit code 1: Error EPERM: CephFS is not offline\nauthenticated with key AQBfake==\nMON KEYRING line")
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 3)
	if got.Status != metav1.ConditionFalse || got.Reason != v1alpha1.ReasonRenameFailed {
		t.Fatalf("condition %+v", got)
	}
	want := failing + ": command terminated with exit code 1: Error EPERM: CephFS is not offline"
	if got.Message != want {
		t.Fatalf("message %q, want %q", got.Message, want)
	}
	wantRan := []string{
		"ceph osd pool rename storage-block bedrock-block",
		"ceph fs fail filesystem-data",
		"ceph fs set filesystem-data refuse_client_session true",
		failing,
	}
	if !slices.Equal(fake.mutations, wantRan) {
		t.Fatalf("ran %v", fake.mutations)
	}
}

func TestAdoptStepReportsAnUnreadableState(t *testing.T) {
	fake := cephFixture(t)
	fake.failures[keyPools] = errors.New("RADOS timed out")
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 3)
	if got.Reason != v1alpha1.ReasonWaitingForQuorum || got.Message != "cannot read the Ceph state: RADOS timed out" {
		t.Fatalf("condition %+v", got)
	}
}

func TestAdoptStepSetsAdoptedWhenNothingIsLeftToRename(t *testing.T) {
	fake := cephFixture(t, allDone(t)...)
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 3)
	if got.Status != metav1.ConditionTrue || got.Reason != v1alpha1.ReasonAdopted {
		t.Fatalf("condition %+v", got)
	}
	if len(fake.mutations) != 0 {
		t.Fatalf("ran %v", fake.mutations)
	}
}

func TestAdoptStepRefusesNamesThatAreNotCephNames(t *testing.T) {
	for _, bad := range []string{"has space", "semi;colon", "$(id)", "slash/name", "-leading"} {
		t.Run(bad, func(t *testing.T) {
			adopt := adoptSpec()
			adopt.Pools.ObjectStore = bad
			fake := cephFixture(t)
			got := adoptStep(context.Background(), fake.run, *adopt, 3)
			if got.Reason != v1alpha1.ReasonRenameFailed || got.Message != "storage.adopt.pools.objectStore is not a valid Ceph name" {
				t.Fatalf("condition %+v", got)
			}
			if len(fake.reads) != 0 || len(fake.mutations) != 0 {
				t.Fatalf("reads %v, mutations %v", fake.reads, fake.mutations)
			}
		})
	}
}

func TestAdoptStepStopsOnAnAdoptedNameThatDoesNotExist(t *testing.T) {
	noObjectStorePools := poolEntries(3, ".mgr", "storage-block", "filesystem-data-metadata", "filesystem-data-data0", ".rgw.root")
	cases := []struct {
		name      string
		mutate    func(*v1alpha1.AdoptSpec)
		overrides []map[string]any
		want      string
	}{
		{"block pool typo", func(a *v1alpha1.AdoptSpec) { a.Pools.Block = "storage-bloc" }, nil, "adopted block pool storage-bloc not found"},
		{"filesystem typo", func(a *v1alpha1.AdoptSpec) { a.Pools.Filesystem = "filesystem-dat" }, nil, "adopted filesystem filesystem-dat not found"},
		{"object store typo", func(a *v1alpha1.AdoptSpec) { a.Pools.ObjectStore = "objectstor" }, nil, "adopted realm objectstor not found"},
		{"realm", nil, []map[string]any{{keyRealms: nameList("realms")}}, "adopted realm objectstore not found"},
		{"zonegroup", nil, []map[string]any{{keyZoneGroups: nameList("zonegroups")}}, "adopted zonegroup objectstore not found"},
		{"zone", nil, []map[string]any{{keyZones: nameList("zones")}}, "adopted zone objectstore not found"},
		{"object store pools", nil, []map[string]any{{keyPools: noObjectStorePools}}, "adopted object store pools objectstore.rgw.* not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adopt := adoptSpec()
			if tc.mutate != nil {
				tc.mutate(adopt)
			}
			fake := cephFixture(t, tc.overrides...)
			got := adoptStep(context.Background(), fake.run, *adopt, 3)
			if got.Status != metav1.ConditionFalse || got.Reason != v1alpha1.ReasonRenameFailed || got.Message != tc.want {
				t.Fatalf("condition %+v, want message %q", got, tc.want)
			}
			if len(fake.mutations) != 0 {
				t.Fatalf("ran %v", fake.mutations)
			}
		})
	}
}

func withPools(base []map[string]any, extra ...string) []map[string]any {
	pools := slices.Clone(base)
	for i, name := range extra {
		pools = append(pools, map[string]any{"pool_id": 100 + i, "pool_name": name, "size": 3})
	}
	return pools
}

func TestAdoptStepRefusesARenameTargetThatExists(t *testing.T) {
	fresh := clusterPools("storage-block", "filesystem-data-metadata", "filesystem-data-data0", "objectstore")
	cases := []struct {
		name      string
		overrides []map[string]any
		want      string
	}{
		{"CephFS metadata pool", []map[string]any{{keyPools: withPools(fresh, "bedrock-fs-metadata")}}, "adopted pool bedrock-fs-metadata target exists"},
		{"CephFS data pool", []map[string]any{{keyPools: withPools(fresh, "bedrock-fs-data0")}}, "adopted pool bedrock-fs-data0 target exists"},
		{"block pool", []map[string]any{{keyPools: withPools(fresh, "bedrock-block")}}, "adopted pool bedrock-block target exists"},
		{"object store pool", []map[string]any{{keyPools: withPools(fresh, "bedrock-rgw.rgw.meta")}}, "adopted pool bedrock-rgw.rgw.meta target exists"},
		{
			"filesystem",
			[]map[string]any{{
				keyFS: []map[string]any{
					{"name": "filesystem-data", "metadata_pool": "filesystem-data-metadata", "data_pools": []string{"filesystem-data-data0"}},
					{"name": "bedrock-fs", "metadata_pool": "other-metadata", "data_pools": []string{"other-data"}},
				},
				keyNewFS: filesystemFlags(true, false),
			}},
			"adopted filesystem bedrock-fs target exists",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := cephFixture(t, tc.overrides...)
			got := adoptStep(context.Background(), fake.run, *adoptSpec(), 3)
			if got.Status != metav1.ConditionFalse || got.Reason != v1alpha1.ReasonRenameFailed || got.Message != tc.want {
				t.Fatalf("condition %+v, want message %q", got, tc.want)
			}
			if len(fake.mutations) != 0 {
				t.Fatalf("a target that exists must stop the step before any command, ran %v", fake.mutations)
			}
		})
	}
}

func TestAdoptStepAcceptsATargetThatIsTheObjectAlreadyRenamed(t *testing.T) {
	halfDone := map[string]any{
		keyPools: clusterPools("bedrock-block", "bedrock-fs-metadata", "filesystem-data-data0", "objectstore"),
		keyFS:    filesystemEntry("bedrock-fs", "bedrock-fs-metadata", "filesystem-data-data0"),
		keyOldFS: nil,
		keyNewFS: filesystemFlags(false, true),
	}
	fake := cephFixture(t, halfDone)
	got := adoptStep(context.Background(), fake.run, *adoptSpec(), 3)
	if got.Reason != v1alpha1.ReasonRenaming || !slices.Contains(fake.mutations, "ceph osd pool rename filesystem-data-data0 bedrock-fs-data0") {
		t.Fatalf("condition %+v, ran %v", got, fake.mutations)
	}
}

func TestAdoptStepSkipsTheNamesThatAreEmpty(t *testing.T) {
	adopt := adoptSpec()
	adopt.Pools = v1alpha1.AdoptPools{}
	fake := cephFixture(t)
	got := adoptStep(context.Background(), fake.run, *adopt, 3)
	if got.Status != metav1.ConditionTrue || got.Reason != v1alpha1.ReasonAdopted || len(fake.mutations) != 0 {
		t.Fatalf("condition %+v, ran %v", got, fake.mutations)
	}
	adopt = adoptSpec()
	adopt.Pools.Block = ""
	fake = cephFixture(t)
	got = adoptStep(context.Background(), fake.run, *adopt, 3)
	if got.Reason != v1alpha1.ReasonRenaming || slices.Contains(fake.mutations, "ceph osd pool rename storage-block bedrock-block") {
		t.Fatalf("condition %+v, ran %v", got, fake.mutations)
	}
}

func TestSafeMessage(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "pool does not exist", "pool does not exist"},
		{"drops key lines of any case", "first\nkeyring: AQB\nSecond KEY here\nthird", "first; third"},
		{"keeps the last lines", "one\ntwo\nthree\nfour\nfive", "three; four; five"},
		{"drops blank lines", "one\n\n  \ntwo\n", "one; two"},
		{"only key lines", "key\nsecret_key: x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeMessage(tc.in); got != tc.want {
				t.Fatalf("%q, want %q", got, tc.want)
			}
		})
	}
}

func writeZoneStub(t *testing.T, getScript string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	zoneGet := filepath.Join(dir, "zone-get.json")
	zoneSet := filepath.Join(dir, "zone-set.json")
	stub := "#!/bin/sh\ncase \"$1 $2\" in\n\"zone get\") " + getScript + " ;;\n\"zone set\") tee \"$ZONE_SET\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "radosgw-admin"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, zoneGet, zoneSet
}

func shellCommand(command renameCommand, dir, zoneGet, zoneSet string) *exec.Cmd {
	return shellArgs(command.Args, dir, zoneGet, zoneSet)
}

func shellArgs(args []string, dir, zoneGet, zoneSet string) *exec.Cmd {
	run := exec.Command(args[0], args[1:]...)
	run.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "ZONE_GET="+zoneGet, "ZONE_SET="+zoneSet)
	return run
}

func zoneWithLookalikes(t *testing.T, old string) (string, []string) {
	t.Helper()
	zone := strings.ReplaceAll(zoneDocument(t), "objectstore.rgw.", old+".rgw.")
	lookalikes := []string{"x" + old + ".rgw.meta"}
	if strings.Contains(old, ".") {
		lookalikes = append(lookalikes, strings.ReplaceAll(old, ".", "X")+".rgw.meta")
	}
	extra, err := json.Marshal(lookalikes)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(zone, `"name": "objectstore"`, `"name": "objectstore", "other_pools": `+string(extra), 1), lookalikes
}

func TestZonePoolRewriteRewritesOnlyThePoolNames(t *testing.T) {
	cases := map[string]string{"objectstore": "objectstore", "dotted store": "my.store"}
	for name, old := range cases {
		t.Run(name, func(t *testing.T) {
			input, lookalikes := zoneWithLookalikes(t, old)
			dir, zoneGet, zoneSet := writeZoneStub(t, `cat "$ZONE_GET"`)
			if err := os.WriteFile(zoneGet, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			run := shellCommand(zonePoolRewrite(old), dir, zoneGet, zoneSet)
			run.Stdout, run.Stderr = &stdout, &stderr
			if err := run.Run(); err != nil {
				t.Fatalf("%v: %s", err, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("the rewrite printed %q, the operator must receive nothing", stdout.String())
			}
			data, err := os.ReadFile(zoneSet)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.ReplaceAll(input, `"`+old+".rgw.", `"bedrock-rgw.rgw.`)
			if strings.TrimSpace(string(data)) != strings.TrimSpace(want) {
				t.Fatalf("zone\n%s\nwant\n%s", data, want)
			}
			if !strings.Contains(string(data), "ZONESECRET") || strings.Contains(string(data), `"`+old+".rgw.") {
				t.Fatalf("zone %s", data)
			}
			for _, lookalike := range lookalikes {
				if !strings.Contains(string(data), `"`+lookalike+`"`) {
					t.Fatalf("lookalike %s changed in %s", lookalike, data)
				}
			}
		})
	}
}

func TestZonePoolRewriteStopsWhenTheZoneCannotBeRead(t *testing.T) {
	dir, zoneGet, zoneSet := writeZoneStub(t, "exit 2")
	run := shellCommand(zonePoolRewrite("objectstore"), dir, zoneGet, zoneSet)
	if err := run.Run(); err == nil {
		t.Fatal("an unreadable zone must fail the command")
	}
	if _, err := os.Stat(zoneSet); err == nil {
		t.Fatal("zone set must not run after a failed zone get")
	}
}

func TestZoneStalePoolsCommandPrintsOnlyThePoolNames(t *testing.T) {
	cases := map[string]string{"objectstore": "objectstore", "dotted store": "my.store"}
	for name, old := range cases {
		t.Run(name, func(t *testing.T) {
			input, _ := zoneWithLookalikes(t, old)
			dir, zoneGet, zoneSet := writeZoneStub(t, `cat "$ZONE_GET"`)
			if err := os.WriteFile(zoneGet, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			run := shellArgs(zoneStalePoolsCommand(old, "bedrock-rgw"), dir, zoneGet, zoneSet)
			run.Stdout = &stdout
			if err := run.Run(); err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, ref := range regexp.MustCompile(`"`+regexp.QuoteMeta(old)+`\.rgw\.[^"]*"`).FindAllString(input, -1) {
				want = append(want, strings.Trim(ref, `"`))
			}
			got := quotedNames(stdout.Bytes())
			if len(want) == 0 || !slices.Equal(got, want) {
				t.Fatalf("names %v, want %v", got, want)
			}
			for _, secret := range []string{"ZONESECRET", "ZONEACCESS", "system_key", "secret_key"} {
				if strings.Contains(stdout.String(), secret) {
					t.Fatalf("the output holds %s: %s", secret, stdout.String())
				}
			}
		})
	}
}

func TestZoneStalePoolsCommandPrintsNothingWhenNoPoolIsStale(t *testing.T) {
	dir, zoneGet, zoneSet := writeZoneStub(t, `cat "$ZONE_GET"`)
	renamed := strings.ReplaceAll(zoneDocument(t), "objectstore.rgw.", "bedrock-rgw.rgw.")
	if err := os.WriteFile(zoneGet, []byte(renamed), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	run := shellArgs(zoneStalePoolsCommand("objectstore", "bedrock-rgw"), dir, zoneGet, zoneSet)
	run.Stdout = &stdout
	if err := run.Run(); err != nil || stdout.Len() != 0 {
		t.Fatalf("error %v, output %q", err, stdout.String())
	}
}

func TestZoneStalePoolsCommandFailsWhenTheZoneCannotBeRead(t *testing.T) {
	dir, zoneGet, zoneSet := writeZoneStub(t, "exit 2")
	run := shellArgs(zoneStalePoolsCommand("objectstore", "bedrock-rgw"), dir, zoneGet, zoneSet)
	if err := run.Run(); err == nil {
		t.Fatal("an unreadable zone must fail the command")
	}
}

func withRealShell(t *testing.T, fake *fakeCeph) (runFunc, *[][]byte, string) {
	t.Helper()
	dir, zoneGet, zoneSet := writeZoneStub(t, `cat "$ZONE_GET"`)
	if err := os.WriteFile(zoneGet, []byte(zoneDocument(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	received := &[][]byte{}
	run := func(ctx context.Context, command []string) ([]byte, error) {
		var out []byte
		var err error
		if command[0] == "sh" {
			out, err = shellArgs(command, dir, zoneGet, zoneSet).Output()
			if command[3] == "zone-pools" {
				fake.reads = append(fake.reads, commandKey(command))
			} else {
				fake.mutations = append(fake.mutations, commandKey(command))
			}
		} else {
			out, err = fake.run(ctx, command)
		}
		*received = append(*received, out)
		return out, err
	}
	return run, received, zoneSet
}

func TestTheZoneSystemKeyNeverReachesTheOperator(t *testing.T) {
	fake := cephFixture(t)
	run, received, zoneSet := withRealShell(t, fake)
	state, err := readCephState(context.Background(), run, *adoptSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(state.ZoneStalePools, zonePoolRefs(t, "objectstore")) {
		t.Fatalf("stale pools %v", state.ZoneStalePools)
	}
	got := adoptStep(context.Background(), run, *adoptSpec(), 3)
	if got.Reason != v1alpha1.ReasonRenaming {
		t.Fatalf("condition %+v", got)
	}
	for _, out := range *received {
		for _, secret := range []string{"ZONESECRET", "ZONEACCESS", "system_key", "secret_key"} {
			if bytes.Contains(out, []byte(secret)) {
				t.Fatalf("the operator received %s in %q", secret, out)
			}
		}
	}
	written, err := os.ReadFile(zoneSet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `"bedrock-rgw.rgw.meta:root"`) || !strings.Contains(string(written), "ZONESECRET") {
		t.Fatalf("the rewrite did not run in the shell: %s", written)
	}
}

func operatorPod(phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-operator-0", Namespace: storageNamespace, Labels: map[string]string{"app": "rook-ceph-operator"}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestOperatorRunReportsTheExitMessageWithoutTheCommandLine(t *testing.T) {
	podError := fmt.Errorf("exec ceph fs rename a b in rook-ceph/pod: %w", fmt.Errorf("%w: %s", errors.New("command terminated with exit code 1"), "Error EPERM"))
	var calls []recordedExec
	_, err := operatorRun(fakeExec("", podError, &calls), "rook-ceph-operator-0")(context.Background(), []string{"ceph", "fs"})
	if err == nil || err.Error() != "command terminated with exit code 1: Error EPERM" {
		t.Fatalf("error %v", err)
	}
	if len(calls) != 1 || calls[0].namespace != "rook-ceph" || calls[0].pod != "rook-ceph-operator-0" || calls[0].container != "rook-ceph-operator" || !slices.Equal(calls[0].command, []string{"ceph", "fs"}) {
		t.Fatalf("calls %+v", calls)
	}
	out, err := operatorRun(fakeExec("answer", nil, &calls), "pod")(context.Background(), []string{"ceph"})
	if err != nil || string(out) != "answer" {
		t.Fatalf("out %q, error %v", out, err)
	}
}

func TestAdvanceAdoptionRunsOnlyInTheStepPhases(t *testing.T) {
	phases := map[string]bool{
		"":                                false,
		v1alpha1.ReasonWaitingForIdentity: false,
		v1alpha1.ReasonWaitingForHosts:    false,
		v1alpha1.ReasonWaitingForQuorum:   true,
		v1alpha1.ReasonWaitingForOSDs:     true,
		v1alpha1.ReasonRenaming:           true,
		v1alpha1.ReasonPoolSizeTooSmall:   true,
		v1alpha1.ReasonRenameFailed:       true,
		v1alpha1.ReasonAdopted:            false,
	}
	for phase, runs := range phases {
		t.Run("phase "+phase, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodRunning)).Build()
			var calls []recordedExec
			got := advanceAdoption(context.Background(), c, fakeExec("", errors.New("no answer"), &calls), adoptInput(phase))
			if runs != (len(calls) > 0) || runs != (len(got) == 1) {
				t.Fatalf("calls %d, conditions %+v", len(calls), got)
			}
		})
	}
}

func TestAdvanceAdoptionRunsInTheRookOperatorPod(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodRunning)).Build()
	var calls []recordedExec
	got := advanceAdoption(context.Background(), c, fakeExec("", errors.New("no answer"), &calls), adoptInput(v1alpha1.ReasonWaitingForQuorum))
	if len(calls) == 0 || calls[0].namespace != "rook-ceph" || calls[0].pod != "rook-ceph-operator-0" || calls[0].container != "rook-ceph-operator" || calls[0].command[0] != "ceph" {
		t.Fatalf("calls %+v", calls)
	}
	if len(got) != 1 || got[0].Type != v1alpha1.ConditionStorageAdopted || got[0].Reason != v1alpha1.ReasonWaitingForQuorum {
		t.Fatalf("conditions %+v", got)
	}
}

func TestAdvanceAdoptionWaitsForTheRookOperatorPod(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodPending)).Build()
	var calls []recordedExec
	got := advanceAdoption(context.Background(), c, fakeExec("", nil, &calls), adoptInput(v1alpha1.ReasonRenaming))
	if len(calls) != 0 || len(got) != 1 || got[0].Reason != v1alpha1.ReasonWaitingForQuorum || got[0].Message != "no running rook-ceph-operator pod in rook-ceph" {
		t.Fatalf("calls %d, conditions %+v", len(calls), got)
	}
}

func TestAdvanceAdoptionDoesNothingWithoutAnAdoptSpec(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonWaitingForQuorum)
	in.Cluster.Spec.Storage.Adopt = nil
	var calls []recordedExec
	if got := advanceAdoption(context.Background(), fake.NewClientBuilder().Build(), fakeExec("", nil, &calls), in); got != nil || len(calls) != 0 {
		t.Fatalf("calls %d, conditions %+v", len(calls), got)
	}
}

func TestAddonReconcilerAdvancesOnlyAfterAnApply(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	var advanced int
	step := metav1.Condition{Type: "Step", Status: metav1.ConditionFalse, Reason: "Stepped"}
	advance := func(context.Context, client.Client, execFunc, AddonInput) []metav1.Condition {
		advanced++
		return []metav1.Condition{step}
	}
	var calls []recordedExec
	cases := []struct {
		name   string
		render func(AddonInput) (Rendered, error)
		exec   execFunc
		want   int
	}{
		{"applied", probeConfigMap, fakeExec("", nil, &calls), 1},
		{"skipped", skipAlways, fakeExec("", nil, &calls), 0},
		{"missing CRD", renderUnknownKind, fakeExec("", nil, &calls), 0},
		{"no exec", probeConfigMap, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			advanced = 0
			r := &AddonReconciler{Client: c, Exec: tc.exec}
			addon := Addon{Name: "step", Condition: "StepReady", Render: tc.render, Advance: advance}
			_, extra := r.reconcileAddon(ctx, addon, AddonInput{})
			if advanced != tc.want || (tc.want == 1) != slices.ContainsFunc(extra, func(c metav1.Condition) bool { return c.Type == "Step" }) {
				t.Fatalf("advanced %d times, extra %+v", advanced, extra)
			}
		})
	}
}
