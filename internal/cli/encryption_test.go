package cli

import (
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
