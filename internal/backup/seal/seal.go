package seal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

const (
	HostKeyDir  = "/etc/bedrock/backup"
	HostKeyFile = HostKeyDir + "/host.key"
	Suffix      = ".age"

	fileMode   = 0o600
	dirMode    = 0o700
	parentMode = 0o755

	secretKeyMarker = "AGE-SECRET-KEY-"
	pluginKeyMarker = "AGE-PLUGIN-"
)

func ParseRecipients(value string) ([]age.Recipient, error) {
	fields := strings.Fields(value)
	recipients := make([]age.Recipient, 0, len(fields))
	for index, field := range fields {
		if holdsSecretKey(field) {
			return nil, fmt.Errorf("recipient %d is an age secret key, not a recipient", index+1)
		}
		recipient, err := age.ParseX25519Recipient(field)
		if err != nil {
			return nil, fmt.Errorf("recipient %d is not an age X25519 recipient", index+1)
		}
		recipients = append(recipients, recipient)
	}
	return recipients, nil
}

func holdsSecretKey(field string) bool {
	upper := strings.ToUpper(field)
	return strings.Contains(upper, secretKeyMarker) || strings.Contains(upper, pluginKeyMarker)
}

func Seal(dst io.Writer, src io.Reader, recipients []age.Recipient) error {
	writer, err := age.Encrypt(dst, recipients...)
	if err != nil {
		return err
	}
	if _, err := io.Copy(writer, src); err != nil {
		return err
	}
	return writer.Close()
}

func Open(src io.Reader, identities []age.Identity) (io.Reader, error) {
	return age.Decrypt(src, identities...)
}

func KeyFile(identity *age.X25519Identity) []byte {
	return fmt.Appendf(nil, "# public key: %s\n%s\n", identity.Recipient(), identity)
}

func EnsureHostIdentity(root string) (*age.X25519Identity, error) {
	path := filepath.Join(root, HostKeyFile)
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	identity, err := readIdentity(path)
	if errors.Is(err, fs.ErrNotExist) {
		return createIdentity(path)
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, fileMode); err != nil {
		return nil, err
	}
	return identity, nil
}

func readIdentity(path string) (*age.X25519Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	identities, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(identities) != 1 {
		return nil, fmt.Errorf("parse %s: want one identity, found %d", path, len(identities))
	}
	identity, ok := identities[0].(*age.X25519Identity)
	if !ok {
		return nil, fmt.Errorf("parse %s: the identity is not X25519", path)
	}
	return identity, nil
}

func createIdentity(path string) (*age.X25519Identity, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	err = writeNewFile(path, KeyFile(identity))
	if errors.Is(err, fs.ErrExist) {
		return readIdentity(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	return identity, nil
}

func writeNewFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".host.key-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := errors.Join(fillSynced(temp, data), temp.Close()); err != nil {
		return err
	}
	if err := os.Link(temp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), parentMode); err != nil {
		return err
	}
	if err := os.Mkdir(dir, dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return os.Chmod(dir, dirMode)
}

func fillSynced(file *os.File, data []byte) error {
	if err := file.Chmod(fileMode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}
