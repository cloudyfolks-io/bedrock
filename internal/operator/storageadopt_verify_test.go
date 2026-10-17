package operator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	keyVersions = "ceph versions -f json"
	keyTree     = "ceph osd tree -f json"
	keyStatus   = "ceph status -f json"
)

var (
	verifyKeys = []string{keyVersions, keyPools, keyTree, keyStatus}
	oldRelease = "ceph version 19.2.3 (c92aebb279828e9c3c1f5d24613efca272649e62) squid (stable)"
	newRelease = "ceph version 20.2.4 (0b1c2d3e4f5a69788796a5b4c3d2e1f001122334) tentacle (stable - RelWithDebInfo)"
)

func adoptedFixture(t *testing.T, overrides ...map[string]any) *fakeCeph {
	t.Helper()
	return fixtureFrom(t, "adopted.json", overrides...)
}

func fakeCephExec(ceph *fakeCeph) execFunc {
	return func(ctx context.Context, _, _, _ string, command []string) ([]byte, error) {
		return ceph.run(ctx, command)
	}
}

func adoptedPools(sizes map[string]int) []map[string]any {
	names := slices.Concat([]string{".mgr", "bedrock-block", "bedrock-fs-metadata", "bedrock-fs-data0", ".rgw.root"}, rgwPoolNames("bedrock-rgw"))
	pools := poolEntries(3, names...)
	for _, pool := range pools {
		if size, found := sizes[pool["pool_name"].(string)]; found {
			pool["size"] = size
		}
	}
	return pools
}

func hostBucket(id int, name string, children ...int) map[string]any {
	return map[string]any{"id": id, "name": name, "type": "host", "type_id": 1, "children": children}
}

func osdLeaf(id int) map[string]any {
	return map[string]any{"id": id, "name": fmt.Sprintf("osd.%d", id), "type": "osd", "type_id": 0, "status": "up"}
}

func treeOf(nodes ...map[string]any) map[string]any {
	root := map[string]any{"id": -1, "name": "default", "type": "root", "type_id": 11, "children": []int{-3}}
	return map[string]any{"nodes": append([]map[string]any{root}, nodes...), "stray": []any{}}
}

func statusWith(health string, extra map[string]any) map[string]any {
	pgmap := map[string]any{"pgs_by_state": []map[string]any{{"state_name": "active+clean", "count": 169}}, "num_pgs": 169}
	for key, value := range extra {
		pgmap[key] = value
	}
	return map[string]any{
		"health": map[string]any{"status": health},
		"osdmap": map[string]any{"num_osds": 4, "num_up_osds": 4},
		"pgmap":  pgmap,
	}
}

func versionsOf(sections map[string]map[string]int) map[string]any {
	out := map[string]any{}
	overall := map[string]int{}
	for section, versions := range sections {
		out[section] = versions
		for version, count := range versions {
			overall[version] += count
		}
	}
	out["overall"] = overall
	return out
}

func problemsOf(t *testing.T, ceph *fakeCeph, change func(*AddonInput)) []string {
	t.Helper()
	in := adoptInput(v1alpha1.ReasonAdopted)
	if change != nil {
		change(&in)
	}
	c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodRunning)).Build()
	return adoptedProblems(context.Background(), c, fakeCephExec(ceph), in)
}

func TestAdoptedProblems(t *testing.T) {
	strayHost := treeOf(hostBucket(-3, "node-1", 0), osdLeaf(0), hostBucket(-5, "old-node-9", 1), osdLeaf(1))
	cases := []struct {
		name      string
		overrides []map[string]any
		change    func(*AddonInput)
		want      []string
	}{
		{"all good", nil, nil, nil},
		{"internal pools may differ", []map[string]any{{keyPools: adoptedPools(map[string]int{".mgr": 1, ".rgw.root": 1})}}, nil, nil},
		{
			"pools that only look like Bedrock pools",
			[]map[string]any{{keyPools: slices.Concat(adoptedPools(nil), poolEntries(1, "bedrock-blockx", "bedrock-fsx-data", "bedrock-fs", "bedrock-rgw", "bedrock-rgwx.rgw.meta"))}},
			nil,
			nil,
		},
		{"an empty host bucket is no problem", []map[string]any{{keyTree: treeOf(hostBucket(-3, "node-1", 0), osdLeaf(0), hostBucket(-9, "retired"))}}, nil, nil},
		{
			"a monitor on the old release",
			[]map[string]any{{keyVersions: versionsOf(map[string]map[string]int{"mon": {oldRelease: 3}, "osd": {newRelease: 4}})}},
			nil,
			[]string{"ceph versions: not on release 20: mon 19.2.3 x3"},
		},
		{
			"an upgrade that stopped half way",
			[]map[string]any{{keyVersions: versionsOf(map[string]map[string]int{"mon": {newRelease: 3}, "osd": {newRelease: 3, oldRelease: 1}, "rgw": {oldRelease: 2}})}},
			nil,
			[]string{"ceph versions: not on release 20: osd 19.2.3 x1, rgw 19.2.3 x2"},
		},
		{
			"a version that names no release",
			[]map[string]any{{keyVersions: versionsOf(map[string]map[string]int{"mon": {"unknown": 1}})}},
			nil,
			[]string{"ceph versions: not on release 20: mon unknown x1"},
		},
		{
			"the block pool is too small",
			[]map[string]any{{keyPools: adoptedPools(map[string]int{"bedrock-block": 2})}},
			nil,
			[]string{"ceph pools differ from storage.replicas 3: bedrock-block (2)"},
		},
		{
			"pools of the filesystem and the object store differ",
			[]map[string]any{{keyPools: adoptedPools(map[string]int{"bedrock-fs-data0": 4, "bedrock-fs-metadata": 1, "bedrock-rgw.rgw.meta": 2})}},
			nil,
			[]string{"ceph pools differ from storage.replicas 3: bedrock-fs-metadata (1), bedrock-fs-data0 (4), bedrock-rgw.rgw.meta (2)"},
		},
		{
			"an OSD sits in a host bucket that is no node",
			[]map[string]any{{keyTree: strayHost}},
			nil,
			[]string{"ceph osd tree: host buckets without a ceph-osd node: old-node-9"},
		},
		{
			"a node lost the ceph-osd role",
			nil,
			func(in *AddonInput) { in.Hosts[0].Spec.Roles = []string{v1alpha1.RoleWorkload} },
			[]string{"ceph osd tree: host buckets without a ceph-osd node: node-3"},
		},
		{"the health is a warning", []map[string]any{{keyStatus: statusWith("HEALTH_WARN", nil)}}, nil, []string{"ceph: health is HEALTH_WARN"}},
		{
			"objects are misplaced",
			[]map[string]any{{keyStatus: statusWith("HEALTH_OK", map[string]any{"misplaced_objects": 12, "misplaced_total": 7224, "misplaced_ratio": 0.00166})}},
			nil,
			[]string{"ceph: 12 objects are misplaced"},
		},
		{
			"every check fails",
			[]map[string]any{{
				keyVersions: versionsOf(map[string]map[string]int{"osd": {oldRelease: 4}}),
				keyPools:    adoptedPools(map[string]int{"bedrock-block": 1}),
				keyTree:     strayHost,
				keyStatus:   statusWith("HEALTH_WARN", map[string]any{"misplaced_objects": 5}),
			}},
			nil,
			[]string{
				"ceph versions: not on release 20: osd 19.2.3 x4",
				"ceph pools differ from storage.replicas 3: bedrock-block (1)",
				"ceph osd tree: host buckets without a ceph-osd node: old-node-9",
				"ceph: health is HEALTH_WARN",
				"ceph: 5 objects are misplaced",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ceph := adoptedFixture(t, tc.overrides...)
			got := problemsOf(t, ceph, tc.change)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("problems %q, want %q", got, tc.want)
			}
			if len(ceph.mutations) != 0 || !slices.Equal(slices.Sorted(slices.Values(ceph.reads)), slices.Sorted(slices.Values(verifyKeys))) {
				t.Fatalf("reads %v, mutations %v", ceph.reads, ceph.mutations)
			}
		})
	}
}

func TestAdoptedProblemsReportsEveryReadThatFails(t *testing.T) {
	for _, key := range verifyKeys {
		t.Run(key, func(t *testing.T) {
			ceph := adoptedFixture(t)
			ceph.failures[key] = errors.New("RADOS timed out")
			got := problemsOf(t, ceph, nil)
			label := strings.Join(strings.Fields(key)[:len(strings.Fields(key))-2], " ")
			if want := []string{"cannot read " + label + ": RADOS timed out"}; !slices.Equal(got, want) {
				t.Fatalf("problems %q, want %q", got, want)
			}
		})
	}
}

func TestAdoptedProblemsStopsAtTheFirstFailedRead(t *testing.T) {
	for i, key := range verifyKeys {
		t.Run(key, func(t *testing.T) {
			ceph := adoptedFixture(t)
			ceph.failures[key] = errors.New("RADOS timed out")
			problemsOf(t, ceph, nil)
			if !slices.Equal(ceph.reads, verifyKeys[:i]) || !slices.Equal(ceph.mutations, []string{key}) {
				t.Fatalf("reads %v, failed attempts %v", ceph.reads, ceph.mutations)
			}
		})
	}
}

func TestAdoptedProblemsKeepsTheProblemsFoundBeforeAFailedRead(t *testing.T) {
	ceph := adoptedFixture(t, map[string]any{
		keyVersions: versionsOf(map[string]map[string]int{"mon": {oldRelease: 3}}),
		keyPools:    adoptedPools(map[string]int{"bedrock-block": 2}),
	})
	ceph.failures[keyTree] = errors.New("RADOS timed out")
	got := problemsOf(t, ceph, nil)
	want := []string{
		"ceph versions: not on release 20: mon 19.2.3 x3",
		"ceph pools differ from storage.replicas 3: bedrock-block (2)",
		"cannot read ceph osd tree: RADOS timed out",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("problems %q, want %q", got, want)
	}
}

func TestAdoptedProblemsKeepsKeyLinesOutOfTheMessage(t *testing.T) {
	ceph := adoptedFixture(t)
	ceph.failures[keyStatus] = errors.New("RADOS timed out\nkey = AQDsecretsecretsecretsecretsecret==")
	got := problemsOf(t, ceph, nil)
	if want := []string{"cannot read ceph status: RADOS timed out"}; !slices.Equal(got, want) {
		t.Fatalf("problems %q, want %q", got, want)
	}
}

func TestAdoptedProblemsReportsOutputThatIsNotJSON(t *testing.T) {
	ceph := adoptedFixture(t, map[string]any{keyTree: "no json"})
	got := problemsOf(t, ceph, nil)
	if len(got) != 1 || !strings.HasPrefix(got[0], "cannot read ceph osd tree: parse the output of ceph osd:") {
		t.Fatalf("problems %q", got)
	}
}

func TestAdoptedProblemsReportsAStatusWithoutHealth(t *testing.T) {
	ceph := adoptedFixture(t, map[string]any{keyStatus: map[string]any{"pgmap": map[string]any{}}})
	got := problemsOf(t, ceph, nil)
	if want := []string{"cannot read ceph status: ceph status has no health"}; !slices.Equal(got, want) {
		t.Fatalf("problems %q, want %q", got, want)
	}
}

func TestAdoptedProblemsNeedsTheRookOperatorPod(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodPending)).Build()
	var calls []recordedExec
	got := adoptedProblems(context.Background(), c, fakeExec("", nil, &calls), adoptInput(v1alpha1.ReasonAdopted))
	if want := []string{"no running rook-ceph-operator pod in rook-ceph"}; !slices.Equal(got, want) || len(calls) != 0 {
		t.Fatalf("problems %q, calls %d", got, len(calls))
	}
}

func TestAdoptedProblemsReadsNothingBeforeAdopted(t *testing.T) {
	for _, phase := range adoptReasons {
		if phase == v1alpha1.ReasonAdopted {
			continue
		}
		t.Run("phase "+phase, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodRunning)).Build()
			var calls []recordedExec
			if got := adoptedProblems(context.Background(), c, fakeExec("", errors.New("no answer"), &calls), adoptInput(phase)); got != nil || len(calls) != 0 {
				t.Fatalf("problems %q, calls %d", got, len(calls))
			}
		})
	}
}

func TestAdoptedProblemsReadsNothingWithoutAnAdoptSpec(t *testing.T) {
	in := storageInput(adoptHosts(), "3")
	c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodRunning)).Build()
	var calls []recordedExec
	if got := adoptedProblems(context.Background(), c, fakeExec("", errors.New("no answer"), &calls), in); got != nil || len(calls) != 0 {
		t.Fatalf("problems %q, calls %d", got, len(calls))
	}
}

func TestAdoptedProblemsReadsNothingWithoutExec(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(operatorPod(corev1.PodRunning)).Build()
	if got := adoptedProblems(context.Background(), c, nil, adoptInput(v1alpha1.ReasonAdopted)); got != nil {
		t.Fatalf("problems %q", got)
	}
}

func TestImageMajorReadsTheReleaseTag(t *testing.T) {
	images := map[string]string{
		"quay.io/ceph/ceph:v20.2.4":                 "20",
		"quay.io/ceph/ceph:v20.2.4@sha256:abcdef":   "20",
		"registry.local:5000/ceph/ceph:v19.2.3":     "19",
		"registry.local:5000/ceph/ceph:20.1.0-rc.1": "20",
		"quay.io/ceph/ceph@sha256:abcdef":           "",
		"quay.io/ceph/ceph:latest":                  "",
		"registry.local:5000/ceph/ceph":             "",
	}
	for image, want := range images {
		t.Run(image, func(t *testing.T) {
			if got, _ := imageMajor(image); got != want {
				t.Fatalf("major %q, want %q", got, want)
			}
		})
	}
}

func TestAdoptedProblemsNamesAnImageWithoutAReleaseTag(t *testing.T) {
	ceph := adoptedFixture(t)
	got := problemsOf(t, ceph, func(in *AddonInput) {
		in.Bundle.Spec.Components[0].Image = "quay.io/ceph/ceph@sha256:abcdef"
	})
	want := []string{`ceph versions: the image "quay.io/ceph/ceph@sha256:abcdef" has no release tag`}
	if !slices.Equal(got, want) {
		t.Fatalf("problems %q, want %q", got, want)
	}
}

func healthyCephCluster() *unstructured.Unstructured {
	return cephClusterWith("Ready", "HEALTH_OK", map[string]any{"ceph version 20.2.4": int64(4)})
}

func TestAdoptedGateReportsOneMessagePerFailure(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.CephProblems = []string{"ceph versions: not on release 20: mon 19.2.3 x3", "ceph: health is HEALTH_WARN"}
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Probes[0].Gate(healthyCephCluster())
	want := release.Readiness{Reason: "VerificationFailed", Message: "ceph versions: not on release 20: mon 19.2.3 x3; ceph: health is HEALTH_WARN"}
	if got != want {
		t.Fatalf("readiness %+v, want %+v", got, want)
	}
}

func TestAdoptedGateKeepsTheMessageOfTheCephCluster(t *testing.T) {
	in := adoptInput(v1alpha1.ReasonAdopted)
	in.CephProblems = []string{"ceph: health is HEALTH_WARN"}
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Probes[0].Gate(cephClusterWith("Ready", "HEALTH_OK", nil))
	want := release.Readiness{Reason: "OSDsNotUp", Message: "rook-ceph has 0 of 4 OSDs up; ceph: health is HEALTH_WARN"}
	if got != want {
		t.Fatalf("readiness %+v, want %+v", got, want)
	}
}

func TestAdoptedGateIsTheStorageGateWithoutProblems(t *testing.T) {
	adopted, err := RenderStorage(adoptInput(v1alpha1.ReasonAdopted))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := RenderStorage(storageInput(adoptHosts(), "3"))
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []*unstructured.Unstructured{
		healthyCephCluster(),
		cephClusterWith("Ready", "HEALTH_WARN", map[string]any{"ceph version 20.2.4": int64(4)}),
		cephClusterWith("Progressing", "", nil),
	} {
		if got, want := adopted.Probes[0].Gate(obj), fresh.Probes[0].Gate(obj); got != want {
			t.Fatalf("readiness %+v, want %+v", got, want)
		}
	}
}

func TestStorageGateIgnoresTheProblemsOfAClusterThatDoesNotAdopt(t *testing.T) {
	in := storageInput(adoptHosts(), "3")
	in.CephProblems = []string{"ceph: health is HEALTH_WARN"}
	out, err := RenderStorage(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Probes[0].Gate(healthyCephCluster()); !got.Ready || got.Message != "" || got.Reason != "" {
		t.Fatalf("readiness %+v", got)
	}
}

func TestAdoptionPendingGateIgnoresTheProblems(t *testing.T) {
	for _, phase := range adoptReasons {
		if phase == v1alpha1.ReasonAdopted {
			continue
		}
		t.Run("phase "+phase, func(t *testing.T) {
			plain := adoptInput(phase)
			checked := adoptInput(phase)
			checked.CephProblems = []string{"ceph: health is HEALTH_WARN"}
			want, err := RenderStorage(plain)
			if err != nil {
				t.Fatal(err)
			}
			got, err := RenderStorage(checked)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Probes) != len(want.Probes) {
				t.Fatalf("probes %d, want %d", len(got.Probes), len(want.Probes))
			}
			for i := range want.Probes {
				if got.Probes[i].Gate(healthyCephCluster()) != want.Probes[i].Gate(healthyCephCluster()) {
					t.Fatalf("gate %+v, want %+v", got.Probes[i].Gate(healthyCephCluster()), want.Probes[i].Gate(healthyCephCluster()))
				}
			}
		})
	}
}

func TestAddonInputReadsTheAdoptedCephClusterOnlyAfterAdopted(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: storageNamespace}}); err != nil {
		t.Fatal(err)
	}
	createRookOperatorPod(t, ctx, c, "rook-ceph-operator-0", corev1.PodRunning)
	bundle := storageInput(nil, "1").Bundle
	cases := []struct {
		name    string
		phase   string
		exec    func(*fakeCeph) execFunc
		problem bool
	}{
		{"adopted", v1alpha1.ReasonAdopted, fakeCephExec, true},
		{"adopted without exec", v1alpha1.ReasonAdopted, func(*fakeCeph) execFunc { return nil }, false},
		{"renaming", v1alpha1.ReasonRenaming, fakeCephExec, false},
		{"not started", "", fakeCephExec, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ceph := adoptedFixture(t)
			cluster := adoptInput(tc.phase).Cluster
			r := &AddonReconciler{Client: c, Bundle: bundle, Exec: tc.exec(ceph)}
			in, err := r.input(ctx, cluster)
			if err != nil {
				t.Fatal(err)
			}
			if tc.problem != slices.ContainsFunc(in.CephProblems, func(problem string) bool {
				return strings.HasPrefix(problem, "ceph pools differ from storage.replicas 1:")
			}) {
				t.Fatalf("problems %q", in.CephProblems)
			}
			if !tc.problem && (len(in.CephProblems) != 0 || len(ceph.reads) != 0) {
				t.Fatalf("problems %q, reads %v", in.CephProblems, ceph.reads)
			}
		})
	}
}
