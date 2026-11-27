package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func TestRestoreOpensAnArchiveSealedToARetiredHostKey(t *testing.T) {
	env := restoreEnv(t, &host.FakeExec{})
	env.Deps.Exec = restoredClusterExec(env.Deps.Root, `{"items":[]}`)
	if _, err := seal.RotateHostIdentity(env.Deps.Root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := restore(context.Background(), env); err != nil {
		t.Fatalf("a restore must open an archive of a retired host key: %v", err)
	}
}
