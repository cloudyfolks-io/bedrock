//go:build e2e

package tenant

import (
	"bytes"
	"context"
	"os"
	"testing"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/cli"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

func newPlainClient(config *rest.Config) (client.Client, error) {
	return client.New(config, client.Options{})
}

func TestJoinFetchesEncryption(t *testing.T) {
	token, err := k0s.DecodeToken(requireEnv(t, "JOIN_TOKEN"))
	if err != nil {
		t.Fatalf("decode the join token: %v", err)
	}
	fetched, err := cli.FetchEncryptionFile(context.Background(), token, newPlainClient)
	if err != nil {
		t.Fatalf("fetch the encryption file: %v", err)
	}
	stored, err := os.ReadFile(encryption.File)
	if err != nil {
		t.Fatalf("read the encryption file of the node: %v", err)
	}
	if !bytes.Equal(fetched, stored) {
		t.Fatalf("the fetched encryption file differs from the file of the node: %d bytes fetched, %d bytes stored", len(fetched), len(stored))
	}
}
