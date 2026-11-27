package operator

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestCommittedOperatorRoleMatchesTheGeneratorExactly(t *testing.T) {
	built := os.Getenv("BEDROCK_RBAC_RELEASE")
	if built == "" {
		t.Skip("set BEDROCK_RBAC_RELEASE to a full built release directory")
	}
	bundle, err := release.Load(os.DirFS(built))
	if err != nil {
		t.Fatal(err)
	}
	role, err := OperatorRole(bundle)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := RoleYAML(role)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatal("run make rbac: the committed operator-rbac.yaml differs from the generator output")
	}
}
