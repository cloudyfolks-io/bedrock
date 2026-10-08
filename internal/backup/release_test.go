package backup

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	awsPlugin      = "docker.io/velero/velero-plugin-for-aws:v1.14.4"
	kubevirtPlugin = "quay.io/kubevirt/kubevirt-velero-plugin:v0.9.1"
)

func TestReleaseComponentIsPinned(t *testing.T) {
	cfg, err := release.LoadBuildConfig("../../release/components.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := release.ComponentConfig{Name: "velero", Order: 55, Chart: "charts/velero", Version: "12.2.0", Namespace: "velero", Values: "release/values/velero.yaml", CRDsToGroup: "crds"}
	found := false
	for _, c := range cfg.Components {
		if c.Name == "velero" {
			found = true
			if c.Order != want.Order || c.Chart != want.Chart || c.Version != want.Version || c.Namespace != want.Namespace || c.Values != want.Values || c.CRDsToGroup != want.CRDsToGroup {
				t.Fatalf("velero component %+v, want %+v", c, want)
			}
		}
	}
	if !found {
		t.Fatal("component velero not found")
	}
	chart, err := os.ReadFile("../../charts/velero/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var meta struct{ Version, AppVersion string }
	if err := yaml.Unmarshal(chart, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Version != "12.2.0" || meta.AppVersion != "1.18.2" {
		t.Fatalf("chart %+v", meta)
	}
}

type veleroValues struct {
	UpgradeCRDs      bool                     `json:"upgradeCRDs"`
	CleanUpCRDs      bool                     `json:"cleanUpCRDs"`
	BackupsEnabled   bool                     `json:"backupsEnabled"`
	SnapshotsEnabled bool                     `json:"snapshotsEnabled"`
	DeployNodeAgent  bool                     `json:"deployNodeAgent"`
	Credentials      struct{ UseSecret bool } `json:"credentials"`
	Configuration    struct {
		Features     string `json:"features"`
		UploaderType string `json:"uploaderType"`
	} `json:"configuration"`
	InitContainers []struct{ Image string } `json:"initContainers"`
}

func TestVeleroValuesEnableTheDataMover(t *testing.T) {
	raw, err := os.ReadFile("../../release/values/velero.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var v veleroValues
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if !v.DeployNodeAgent || v.UpgradeCRDs || v.CleanUpCRDs || v.BackupsEnabled || v.SnapshotsEnabled || v.Credentials.UseSecret {
		t.Fatalf("values %+v", v)
	}
	if v.Configuration.Features != "EnableCSI" || v.Configuration.UploaderType != "kopia" {
		t.Fatalf("configuration %+v", v.Configuration)
	}
	images := []string{}
	for _, c := range v.InitContainers {
		images = append(images, c.Image)
	}
	slices.Sort(images)
	if !slices.Equal(images, []string{awsPlugin, kubevirtPlugin}) {
		t.Fatalf("plugins %v", images)
	}
}

func TestVeleroChartRendersWithTheValues(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	cmd := exec.Command("helm", "template", "velero", "charts/velero", "--include-crds", "--namespace", "velero", "-f", "release/values/velero.yaml")
	cmd.Dir = "../.."
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template: %v: %s", err, stderr.String())
	}
	rendered := out.String()
	counts := map[string]int{}
	for _, doc := range strings.Split(rendered, "\n---") {
		var obj struct {
			Kind     string
			Metadata struct{ Name string }
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		counts[obj.Kind]++
		if obj.Kind == "Deployment" && obj.Metadata.Name == "velero" {
			for _, image := range []string{awsPlugin, kubevirtPlugin} {
				if !strings.Contains(doc, "image: "+image) {
					t.Fatalf("deployment misses %s", image)
				}
			}
		}
	}
	if counts["Deployment"] != 1 || counts["DaemonSet"] != 1 || counts["Job"] != 0 || counts["CustomResourceDefinition"] < 12 {
		t.Fatalf("rendered kinds %v", counts)
	}
}
