package login

import (
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

func TestAttemptKeyHasAFixedSize(t *testing.T) {
	short := AttemptKey("203.0.113.7", "alice")
	huge := AttemptKey("203.0.113.7", strings.Repeat("x", 64<<10))
	if len(short) != len(huge) {
		t.Fatalf("key sizes differ: %d and %d", len(short), len(huge))
	}
	if want := "203.0.113.7|" + secret.SHA256Hex("alice"); short != want {
		t.Fatalf("key = %q, want %q", short, want)
	}
	if AttemptKey("203.0.113.7", "alice") == AttemptKey("203.0.113.8", "alice") || AttemptKey("203.0.113.7", "alice") == AttemptKey("203.0.113.7", "bob") {
		t.Fatal("the key must keep the address and the username apart")
	}
}
