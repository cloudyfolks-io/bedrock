package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const ssOutput = `LISTEN 0      128          0.0.0.0:22         0.0.0.0:*
LISTEN 0      4096      10.0.10.11:3300       0.0.0.0:*
LISTEN 0      4096   127.0.0.53%lo:53         0.0.0.0:*
LISTEN 0      4096               *:6789             *:*
LISTEN 0      128             [::]:22            [::]:*
LISTEN 0      4096           [::1]:9100         [::]:*
this line is not a socket
LISTEN 0      4096       127.0.0.1:notaport       0.0.0.0:*
`

func rookRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, "var", "lib", "rook", path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGatherAdopt(t *testing.T) {
	root := rookRoot(t, map[string]string{
		"mon-a/data/store.db/CURRENT": "x",
		"rook-ceph/rook-ceph.config":  "[global]\nfsid                      = " + sampleFSID + "\nmon_host = 10.0.10.11\n",
	})
	e := &FakeExec{Responses: map[string]string{"ss -H -ltn": ssOutput}}
	facts, err := GatherAdopt(context.Background(), e, root, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !facts.MonStores["a"] || facts.MonStores["b"] {
		t.Fatalf("mon stores %v", facts.MonStores)
	}
	if facts.ConfigFSID != sampleFSID {
		t.Fatalf("config fsid %q", facts.ConfigFSID)
	}
	slices.Sort(facts.ListeningPorts)
	if !slices.Equal(facts.ListeningPorts, []int{22, 22, 53, 3300, 6789, 9100}) {
		t.Fatalf("ports %v", facts.ListeningPorts)
	}
}

func TestGatherAdoptConfigVariants(t *testing.T) {
	cases := map[string]struct{ content, want string }{
		"no spaces":       {"[global]\nfsid=" + sampleFSID + "\n", sampleFSID},
		"tabs and blanks": {"\n[global]\n\tfsid\t=\t" + sampleFSID + "  \n", sampleFSID},
		"other keys only": {"[global]\nmon_host = 10.0.10.11\nfsid_other = x\n", ""},
		"empty":           {"", ""},
	}
	for name, tc := range cases {
		root := rookRoot(t, map[string]string{"rook-ceph/rook-ceph.config": tc.content})
		e := &FakeExec{Responses: map[string]string{"ss -H -ltn": ""}}
		facts, err := GatherAdopt(context.Background(), e, root, nil)
		if err != nil || facts.ConfigFSID != tc.want {
			t.Fatalf("%s: fsid %q err %v", name, facts.ConfigFSID, err)
		}
	}
}

func TestGatherAdoptMissingFiles(t *testing.T) {
	e := &FakeExec{Responses: map[string]string{"ss -H -ltn": ""}}
	facts, err := GatherAdopt(context.Background(), e, t.TempDir(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if facts.MonStores["a"] || facts.ConfigFSID != "" || len(facts.ListeningPorts) != 0 {
		t.Fatalf("facts %+v", facts)
	}
}

func TestGatherAdoptUnreadableConfig(t *testing.T) {
	root := rookRoot(t, map[string]string{"rook-ceph/rook-ceph.config/inner": "x"})
	e := &FakeExec{Responses: map[string]string{"ss -H -ltn": ""}}
	if _, err := GatherAdopt(context.Background(), e, root, nil); err == nil {
		t.Fatal("a config path that cannot be read must be an error")
	}
}

func TestGatherAdoptSocketListFails(t *testing.T) {
	e := &FakeExec{Errors: map[string]error{"ss -H -ltn": errors.New(`exec: "ss": executable file not found in $PATH`)}}
	if _, err := GatherAdopt(context.Background(), e, t.TempDir(), nil); err == nil {
		t.Fatal("missing ss must be an error")
	}
}
