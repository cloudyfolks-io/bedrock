package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func document(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "network.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDocsMentionEveryReason(t *testing.T) {
	text := document(t)
	for _, reason := range AllReasons {
		if !strings.Contains(text, "`"+reason+"`") {
			t.Errorf("docs/network.md does not describe the reason %s", reason)
		}
	}
}

func TestDocsMentionEverySetting(t *testing.T) {
	text := document(t)
	for _, key := range networkKeys {
		if !strings.Contains(text, "`"+key+"`") {
			t.Errorf("docs/network.md does not describe the setting %s", key)
		}
	}
}

func TestDocsStatePodContractStrings(t *testing.T) {
	text := document(t)
	annotations, labels := PodNetworkMetadata([]NIC{
		{Namespace: "t1-prod", Subnet: "default", MAC: "02:00:00:00:00:01", SecurityGroups: []string{"web"}},
		{Namespace: "t1-prod", Subnet: "backend"},
	})
	for key := range annotations {
		if strings.HasPrefix(key, "default.") && !strings.Contains(text, "`"+key+":") {
			t.Errorf("docs/network.md does not state the annotation %s", key)
		}
	}
	for key := range labels {
		if !strings.Contains(text, key+`: "true"`) && !strings.Contains(text, "sg.bedrock.cloudyfolks.io/<group>") {
			t.Errorf("docs/network.md does not state the label %s", key)
		}
	}
	if _, found := annotations["backend.t1-prod.fabric.cloudyfolks.io/default_route"]; !found {
		t.Fatal("the second NIC must carry default_route")
	}
	if !strings.Contains(text, "default_route") {
		t.Error("docs/network.md does not state default_route")
	}
}
