package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/host"
)

type startSpy struct {
	host.Exec
	root    string
	atStart []string
}

func (s *startSpy) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "start" {
		entries, _ := os.ReadDir(filepath.Join(s.root, k0sImagesDir))
		for _, entry := range entries {
			s.atStart = append(s.atStart, entry.Name())
		}
	}
	return s.Exec.Run(ctx, name, args...)
}

func restoreWithArchive(t *testing.T, keepStore bool) []string {
	t.Helper()
	env := restoreEnv(t, &host.FakeExec{})
	root := env.Deps.Root
	if !keepStore {
		if err := os.RemoveAll(filepath.Join(root, "var/lib/k0s/containerd")); err != nil {
			t.Fatal(err)
		}
	}
	writeArchived(t, root, "v0.2.0", "a.tar", "k0s-airgap.tar")
	writeArchived(t, root, "v0.3.0", "b.tar")
	spy := &startSpy{Exec: restoredClusterExec(root, `{"items":[]}`), root: root}
	env.Deps.Exec = spy
	if _, err := restore(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	return spy.atStart
}

func TestRestoreLinksTheOldVersionImagesBeforeK0sStartsOnAnEmptyStore(t *testing.T) {
	got := restoreWithArchive(t, false)
	slices.Sort(got)
	if want := []string{"v0.2.0-a.tar", "v0.2.0-k0s-airgap.tar"}; !slices.Equal(got, want) {
		t.Fatalf("linked at k0s start %v, want %v", got, want)
	}
}

func TestRestoreRecordsTheLinksItMakes(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	root := env.Deps.Root
	if err := os.RemoveAll(filepath.Join(root, "var/lib/k0s/containerd")); err != nil {
		t.Fatal(err)
	}
	writeArchived(t, root, "v0.2.0", "a.tar")
	env.Deps.Exec = restoredClusterExec(root, `{"items":[]}`)
	if _, err := restore(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if got := readLinkRecord(root); !slices.Equal(got, []string{"v0.2.0-a.tar"}) {
		t.Fatalf("record %v", got)
	}
}

func TestRestoreLinksNothingWhenContainerdHoldsTheImages(t *testing.T) {
	if got := restoreWithArchive(t, true); len(got) != 0 {
		t.Fatalf("k0s must start with an empty images directory, found %v", got)
	}
}
