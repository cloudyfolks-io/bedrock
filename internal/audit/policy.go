package audit

import (
	"fmt"

	sigyaml "sigs.k8s.io/yaml"
)

const bedrockGroup = "bedrock.cloudyfolks.io"

var readVerbs = []string{"get", "list", "watch"}

var writeVerbs = []string{"create", "update", "patch", "delete", "deletecollection"}

var secretBearingKinds = []string{"apitokens", "authcodes", "authrequests", "credentials", "devicerequests", "refreshtokens", "sessions", "signingkeys", "*/status"}

func Policy(readLevel string) ([]byte, error) {
	if readLevel != "Metadata" && readLevel != "RequestResponse" {
		return nil, fmt.Errorf("audit level %q is not Metadata or RequestResponse", readLevel)
	}
	policy := map[string]any{
		"apiVersion": "audit.k8s.io/v1",
		"kind":       "Policy",
		"omitStages": []string{"RequestReceived"},
		"rules":      rules(readLevel),
	}
	return sigyaml.Marshal(policy)
}

func rules(readLevel string) []map[string]any {
	return []map[string]any{
		{"level": "None", "nonResourceURLs": []string{"/healthz*", "/livez*", "/readyz*", "/version", "/metrics"}},
		{"level": "None", "resources": []map[string]any{
			{"group": "", "resources": []string{"events"}},
			{"group": "coordination.k8s.io", "resources": []string{"leases"}},
		}},
		{"level": "None", "userGroups": []string{"system:nodes"}, "verbs": readVerbs},
		{"level": "Metadata", "resources": []map[string]any{
			{"group": "", "resources": []string{"secrets", "configmaps", "serviceaccounts/token"}},
			{"group": "authentication.k8s.io", "resources": []string{"tokenreviews"}},
			{"group": bedrockGroup, "resources": secretBearingKinds},
		}},
		{"level": "RequestResponse", "resources": []map[string]any{{"group": bedrockGroup}}, "verbs": writeVerbs},
		{"level": readLevel, "verbs": readVerbs},
		{"level": "Metadata"},
	}
}
