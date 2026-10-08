package operator

import (
	"context"
	"crypto/rand"
	"regexp"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func TestEnsureAuthnMaterialCreatesTheConsoleSecret(t *testing.T) {
	c, _ := StartTestEnv(t)
	authnNamespaces(t, c)
	ctx := context.Background()
	if err := ensureAuthnMaterial(ctx, c, rand.Reader, time.Now()); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: "bedrock-console-client"}
	first := readSecret(t, c, key)
	if !regexp.MustCompile(`^[0-9a-zA-Z]{32}$`).Match(first.Data["clientSecret"]) || first.Labels[v1alpha1.LabelAuthn] != "true" {
		t.Fatalf("secret %v %v", first.Data, first.Labels)
	}
	if err := ensureAuthnMaterial(ctx, c, rand.Reader, time.Now()); err != nil {
		t.Fatal(err)
	}
	if again := readSecret(t, c, key); again.ResourceVersion != first.ResourceVersion || string(again.Data["clientSecret"]) != string(first.Data["clientSecret"]) {
		t.Fatal("the second run changed the console client Secret")
	}
}
