package release

import (
	"os/exec"
	"strings"
	"testing"
)

func runCheckPins(t *testing.T, release string, pinned bool) (string, error) {
	t.Helper()
	dir := writeTree(t, map[string]string{"release.yaml": release})
	command := exec.Command("bash", "../../hack/check-release-pins.sh", dir)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	if pinned {
		command.Env = append(command.Env, "REQUIRE_PINNED_IMAGE=1")
	}
	out, err := command.CombinedOutput()
	return string(out), err
}

func TestCheckReleasePinsFailsOnEmptyUpgradeFrom(t *testing.T) {
	for name, release := range map[string]string{
		"missing": "version: v1.0.0\nimage: " + ownImage + "\n",
		"empty":   "version: v1.0.0\nupgradeFrom: []\nimage: " + ownImage + "\n",
		"blank":   "version: v1.0.0\nupgradeFrom:\nimage: " + ownImage + "\n",
	} {
		out, err := runCheckPins(t, release, false)
		if err == nil || !strings.Contains(out, "upgradeFrom is empty") {
			t.Errorf("%s: err %v output %q", name, err, out)
		}
	}
}

func TestCheckReleasePinsAcceptsAListedVersion(t *testing.T) {
	out, err := runCheckPins(t, "version: v1.0.0\nupgradeFrom:\n- v0.5.0\nimage: "+ownImage+"\n", false)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestCheckReleasePinsWantsADigestWhenAsked(t *testing.T) {
	listed := "version: v1.0.0\nupgradeFrom:\n- v0.5.0\n"
	if out, err := runCheckPins(t, listed+"image: "+ownImage+"\n", true); err == nil || !strings.Contains(out, "not pinned") {
		t.Fatalf("a tag must fail: %v %q", err, out)
	}
	if out, err := runCheckPins(t, listed+"image: "+ownImage+"@"+ownDigest+"\n", true); err != nil {
		t.Fatalf("a digest must pass: %v %s", err, out)
	}
}
