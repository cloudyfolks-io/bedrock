package seal

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func newIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

func sealBytes(t *testing.T, plain []byte, recipients ...age.Recipient) []byte {
	t.Helper()
	var sealed bytes.Buffer
	if err := Seal(&sealed, bytes.NewReader(plain), recipients); err != nil {
		t.Fatal(err)
	}
	return sealed.Bytes()
}

func openBytes(sealed []byte, identity age.Identity) ([]byte, error) {
	reader, err := Open(bytes.NewReader(sealed), []age.Identity{identity})
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func TestSealOpenRoundTrip(t *testing.T) {
	first, second, stranger := newIdentity(t), newIdentity(t), newIdentity(t)
	plain := randomBytes(t, 3<<20)
	sealed := sealBytes(t, plain, first.Recipient(), second.Recipient())

	for name, identity := range map[string]*age.X25519Identity{"first": first, "second": second} {
		opened, err := openBytes(sealed, identity)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(opened, plain) {
			t.Fatalf("%s: opened bytes differ from the plaintext", name)
		}
	}
	if _, err := openBytes(sealed, stranger); err == nil {
		t.Fatal("a stranger identity opened the archive")
	}
}

func TestSealWithoutRecipientsFails(t *testing.T) {
	var sealed bytes.Buffer
	if err := Seal(&sealed, strings.NewReader("data"), nil); err == nil {
		t.Fatal("Seal accepted an empty recipient list")
	}
}

func TestSealedBytesAreNotPlaintext(t *testing.T) {
	marker := randomBytes(t, 32)
	plain := append(append([]byte{}, marker...), randomBytes(t, 1<<10)...)
	sealed := sealBytes(t, plain, newIdentity(t).Recipient())

	if !bytes.HasPrefix(sealed, []byte("age-encryption.org/v1")) {
		t.Fatalf("sealed output starts with %q", sealed[:32])
	}
	if bytes.Contains(sealed, marker) {
		t.Fatal("sealed output contains plaintext")
	}
}

func TestParseRecipients(t *testing.T) {
	first, second := newIdentity(t), newIdentity(t)

	for _, blank := range []string{"", " \n "} {
		recipients, err := ParseRecipients(blank)
		if err != nil || len(recipients) != 0 {
			t.Fatalf("%q gave %d recipients and error %v", blank, len(recipients), err)
		}
	}

	recipients, err := ParseRecipients(first.Recipient().String() + "\n  " + second.Recipient().String() + " \n")
	if err != nil || len(recipients) != 2 {
		t.Fatalf("got %d recipients and error %v", len(recipients), err)
	}

	for _, bad := range []string{"ssh-ed25519", "age1bad"} {
		value := first.Recipient().String() + "\n" + bad + " AAAAC3NzaC1lZDI1NTE5AAAAIexample"
		if _, err := ParseRecipients(value); err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatalf("%q gave error %v", bad, err)
		}
	}
}

func TestEnsureHostIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, HostKeyFile)

	created, err := EnsureHostIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("file mode %v, directory mode %v", fileInfo.Mode().Perm(), dirInfo.Mode().Perm())
	}

	again, err := EnsureHostIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	if again.String() != created.String() {
		t.Fatal("the second call returned another identity")
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %d entries, error %v", len(entries), err)
	}
}

func TestEnsureHostIdentityKeepsGarbage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, HostKeyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("not an identity\n")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHostIdentity(root); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("error %v does not name %s", err, path)
	}
	kept, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(kept, garbage) {
		t.Fatalf("the file changed: %q, error %v", kept, err)
	}
}

func TestKeyFile(t *testing.T) {
	identity := newIdentity(t)
	lines := strings.Split(strings.TrimSuffix(string(KeyFile(identity)), "\n"), "\n")

	if len(lines) != 2 || lines[0] != "# public key: "+identity.Recipient().String() || lines[1] != identity.String() {
		t.Fatalf("unexpected key file lines %q", lines)
	}
	parsed, err := age.ParseIdentities(bytes.NewReader(KeyFile(identity)))
	if err != nil || len(parsed) != 1 {
		t.Fatalf("parsed %d identities, error %v", len(parsed), err)
	}
	sealed := sealBytes(t, []byte("data"), identity.Recipient())
	if opened, err := openBytes(sealed, parsed[0]); err != nil || string(opened) != "data" {
		t.Fatalf("parsed identity opened %q, error %v", opened, err)
	}
}
