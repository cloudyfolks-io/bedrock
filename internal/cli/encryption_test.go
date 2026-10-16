package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireSockets(t *testing.T) {
	root := "/host"
	statAs := func(info fs.FileInfo, err error) func(string) (fs.FileInfo, error) {
		return func(path string) (fs.FileInfo, error) {
			if path != filepath.Join(root, "run", "kms", "kms.sock") {
				t.Fatalf("stat %s", path)
			}
			return info, err
		}
	}
	paths := []string{"/run/kms/kms.sock"}
	cases := []struct {
		name    string
		stat    func(string) (fs.FileInfo, error)
		paths   []string
		wantErr string
	}{
		{name: "socket", stat: statAs(devInfo{fs.ModeSocket | 0o660}, nil), paths: paths},
		{name: "no paths", stat: statAs(nil, errors.New("not called")), paths: nil},
		{name: "regular file", stat: statAs(devInfo{0o600}, nil), paths: paths, wantErr: "kms plugin socket /run/kms/kms.sock is missing: start the KMS plugin on this node first"},
		{name: "stat error", stat: statAs(nil, fs.ErrNotExist), paths: paths, wantErr: "kms plugin socket /run/kms/kms.sock is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSockets(tc.stat, root, tc.paths)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("got %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestChooseEncryption(t *testing.T) {
	const path = "/var/lib/k0s/pki/encryption.yaml"
	existing := []byte("existing")
	fresh := []byte("fresh")
	cases := []struct {
		name          string
		facts         encryptionFacts
		want          []byte
		wantGenerated bool
		wantErr       string
	}{
		{name: "new node with a file resumes", facts: encryptionFacts{path: path, content: existing, found: true}, want: existing},
		{name: "new node without a file makes a key", facts: encryptionFacts{path: path}, want: fresh, wantGenerated: true},
		{name: "installed node names the file and has it", facts: encryptionFacts{path: path, content: existing, found: true, installed: true, named: true}, want: existing},
		{name: "installed node names a missing file", facts: encryptionFacts{path: path, installed: true, named: true}, wantErr: path + " is missing but k0s.yaml names it: restore it from Secret kube-system/bedrock-secrets-encryption or a k0s backup"},
		{name: "installed node has a file k0s.yaml does not name", facts: encryptionFacts{path: path, content: existing, found: true, installed: true}, wantErr: path + " exists but k0s.yaml does not name it: init does not enable encryption on an installed node, remove the file"},
		{name: "installed node without encryption", facts: encryptionFacts{path: path, installed: true}},
		{name: "empty file on a new node", facts: encryptionFacts{path: path, content: []byte{}, found: true}, wantErr: path + " is empty"},
		{name: "empty file on an installed node", facts: encryptionFacts{path: path, content: []byte{}, found: true, installed: true, named: true}, wantErr: path + " is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generated := false
			got, err := chooseEncryption(tc.facts, func() ([]byte, error) {
				generated = true
				return fresh, nil
			})
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("got %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.want) || (tc.want == nil) != (got == nil) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if generated != tc.wantGenerated {
				t.Fatalf("generated %v, want %v", generated, tc.wantGenerated)
			}
		})
	}
}
