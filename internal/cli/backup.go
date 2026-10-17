package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

const (
	backupUsage  = "usage: bedrock backup keygen | decrypt --in <path> --out <path|-> [--identity <path>]"
	decryptUsage = "usage: bedrock backup decrypt --in <path> --out <path|-> [--identity <path>]"
	keygenUsage  = "usage: bedrock backup keygen"
	stdoutPath   = "-"
)

func backupCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, backupUsage)
		return 2
	}
	switch args[0] {
	case "keygen":
		return backupKeygen(args[1:], stdout, stderr)
	case "decrypt":
		return RunBackupDecrypt(args[1:], "/", stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown backup command: %s\n", args[0])
	return 2
}

func backupKeygen(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("backup keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, keygenUsage)
		return 2
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return fail(stderr, err)
	}
	if _, err := stdout.Write(seal.KeyFile(identity)); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func RunBackupDecrypt(args []string, root string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("backup decrypt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	in := flags.String("in", "", "sealed archive path")
	out := flags.String("out", "", "plain archive path, or - for stdout")
	identity := flags.String("identity", filepath.Join(root, seal.HostKeyFile), "age identity file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *in == "" || *out == "" {
		fmt.Fprintln(stderr, decryptUsage)
		return 2
	}
	if err := decryptArchive(*in, *out, *identity, stdout); err != nil {
		return fail(stderr, fmt.Errorf("decrypt %s: %w", *in, err))
	}
	return 0
}

func decryptArchive(in, out, identityPath string, stdout io.Writer) error {
	identities, err := readIdentities(identityPath)
	if err != nil {
		return err
	}
	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer src.Close()
	plain, err := seal.Open(src, identities)
	if err != nil {
		return err
	}
	if out == stdoutPath {
		_, err := io.Copy(stdout, plain)
		return err
	}
	return writeFileAtomic(out, plain)
}

func readIdentities(path string) ([]age.Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity: %w", err)
	}
	identities, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("identity file holds no valid age identity")
	}
	return identities, nil
}

func writeFileAtomic(path string, src io.Reader) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".decrypt-*.partial")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := io.Copy(temp, src); err != nil {
		return errors.Join(err, temp.Close())
	}
	if err := errors.Join(temp.Sync(), temp.Close()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
