package settings

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildCatalogKeysAndDefaults(t *testing.T) {
	want := map[string]string{
		"build.git-image":        "alpine/git:v2.54.0",
		"build.buildkit-image":   "moby/buildkit:v0.31.2",
		"build.builder-image":    "paketobuildpacks/builder-jammy-base:0.4.640",
		"build.run-image":        "paketobuildpacks/run-jammy-base:0.1.275",
		"build.default-timeout":  "30m",
		"build.max-timeout":      "2h",
		"build.job-ttl":          "1h",
		"build.registry-mirrors": "",
	}
	for key, value := range want {
		def, ok := Lookup(key)
		if !ok || def.Default != value || def.Description == "" {
			t.Errorf("setting %q: %+v", key, def)
		}
	}
	for _, key := range []string{"build.git-image", "build.buildkit-image", "build.builder-image", "build.run-image"} {
		if err := def(t, key).Validate(def(t, key).Default); err != nil {
			t.Errorf("%s default: %v", key, err)
		}
	}
}

func def(t *testing.T, key string) Definition {
	t.Helper()
	found, ok := Lookup(key)
	if !ok {
		t.Fatalf("missing %s", key)
	}
	return found
}

func TestBuildImageSettingsValidation(t *testing.T) {
	for _, key := range []string{"build.git-image", "build.buildkit-image", "build.builder-image", "build.run-image"} {
		for _, good := range []string{"", "alpine/git:v2.54.0", "registry.test:5000/mirror/git:1", "registry.test/a/b@sha256:2f5adac4ecd194d9f8c10b7b5d7bceb5186853db1b26e5abd3a657af0b7e26ec"} {
			if err := validates(t, key, good); err != nil {
				t.Errorf("%s %q: %v", key, good, err)
			}
		}
		for _, bad := range []string{"alpine/git", "Alpine/Git:1", "alpine git:1", "https://registry.test/a:1", "a:b:c", "alpine/git:"} {
			if err := validates(t, key, bad); err == nil {
				t.Errorf("%s %q passed", key, bad)
			}
		}
	}
}

func TestBuildDurationSettingsValidation(t *testing.T) {
	for _, key := range []string{"build.default-timeout", "build.max-timeout", "build.job-ttl"} {
		for _, good := range []string{"", "1m", "30m", "2h", "24h"} {
			if err := validates(t, key, good); err != nil {
				t.Errorf("%s %q: %v", key, good, err)
			}
		}
		for _, bad := range []string{"59s", "24h1s", "0", "-1h", "soon", "90m30ms"} {
			if err := validates(t, key, bad); err == nil {
				t.Errorf("%s %q passed", key, bad)
			}
		}
	}
}

func TestBuildRegistryMirrorsValidation(t *testing.T) {
	good := []string{
		"",
		"docker.io=registry.test/mirror-docker",
		"docker.io=registry.test/mirror-docker,ghcr.io=registry.test/mirror-ghcr",
		"docker.io=registry.test:5000/mirror/docker",
		"docker.io=10.100.0.220:5000/mirror",
	}
	for _, value := range good {
		if err := validates(t, "build.registry-mirrors", value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	bad := []string{
		"docker.io", "docker.io=", "=registry.test/a", "docker.io=registry.test", "docker.io=registry.test/",
		"docker.io=https://registry.test/a", "docker.io=registry.test/a b", "docker.io=registry.test/A",
		"docker.io=registry.test/a,", ",docker.io=registry.test/a", "docker.io=registry.test/a, ghcr.io=registry.test/b",
		"http://docker.io=registry.test/a", "docker.io:=registry.test/a", "docker.io=registry.test:x/a",
	}
	for _, value := range bad {
		if err := validates(t, "build.registry-mirrors", value); err == nil {
			t.Errorf("%q passed", value)
		}
	}
}

func TestParseRegistryMirrorsKeepsTheOrderOfTheEntries(t *testing.T) {
	got, err := ParseRegistryMirrors("quay.io=r.test/q,docker.io=r.test/d")
	if err != nil {
		t.Fatal(err)
	}
	want := []RegistryMirror{{"quay.io", "r.test/q"}, {"docker.io", "r.test/d"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
	empty, err := ParseRegistryMirrors("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty gives %+v, %v", empty, err)
	}
	_, err = ParseRegistryMirrors("docker.io=r.test/a b")
	if err == nil || strings.Contains(err.Error(), "r.test") {
		t.Fatalf("the error must not repeat the value: %v", err)
	}
}

func TestDocsListEveryBuildSetting(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "builds.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range Catalog() {
		if strings.HasPrefix(definition.Key, "build.") && !strings.Contains(string(raw), "| `"+definition.Key+"` | "+docDefault(definition.Default)+" |") {
			t.Errorf("docs/builds.md has no table row for %s with its default", definition.Key)
		}
	}
}

func docDefault(value string) string {
	if value == "" {
		return "empty"
	}
	return "`" + value + "`"
}

func TestLabScriptUsesTheDefaultBuildImages(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "hack", "lab-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"build.git-image", "build.buildkit-image", "build.builder-image"} {
		if image := def(t, key).Default; !strings.Contains(string(raw), "="+image+"\n") {
			t.Errorf("hack/lab-build.sh does not use the default %s %s", key, image)
		}
	}
}
