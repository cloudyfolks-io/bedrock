package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func TestCheckBundle(t *testing.T) {
	spec := release.BundleSpec{Version: "v0.3.0", Arch: "amd64"}
	cases := []struct {
		name    string
		arch    string
		version string
		want    string
	}{
		{"match", "amd64", "v0.3.0", ""},
		{"foreign arch", "arm64", "v0.3.0", "bundle is for amd64, this host is arm64"},
		{"other version", "amd64", "v0.4.0", "bundle is version v0.3.0, want v0.4.0"},
	}
	for _, tc := range cases {
		err := checkBundle(spec, tc.arch, tc.version)
		if tc.want == "" && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if tc.want != "" && (err == nil || err.Error() != tc.want) {
			t.Fatalf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestOpenBundleRejectsForeignArch(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, release.BundleFileName), []byte("version: v0.3.0\narch: amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle.tar.zst")
	if err := release.PackBundle(src, out); err != nil {
		t.Fatal(err)
	}
	_, err := openBundle(initOptions{bundle: out, workDir: t.TempDir()}, "", "arm64", "v0.3.0")
	if err == nil || !strings.Contains(err.Error(), "bundle is for amd64, this host is arm64") {
		t.Fatalf("error %v", err)
	}
}
