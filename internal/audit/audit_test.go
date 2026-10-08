package audit

import (
	"maps"
	"slices"
	"strings"
	"testing"

	auditv1 "k8s.io/apiserver/pkg/apis/audit"
	auditpkg "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/audit/policy"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

func evaluator(t *testing.T, level string) auditpkg.PolicyRuleEvaluator {
	t.Helper()
	raw, err := Policy(level)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := policy.LoadPolicyFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return policy.NewPolicyRuleEvaluator(loaded)
}

func TestPolicyParsesWithTheAPIServerValidator(t *testing.T) {
	for _, level := range []string{"Metadata", "RequestResponse"} {
		evaluator(t, level)
	}
}

func TestPolicyRejectsAnUnknownLevel(t *testing.T) {
	for _, level := range []string{"None", "", "Request"} {
		if _, err := Policy(level); err == nil {
			t.Errorf("%q accepted", level)
		}
	}
}

func TestPolicyLevels(t *testing.T) {
	const bedrock = "bedrock.cloudyfolks.io"
	cases := []struct {
		verb, group, resource, readLevel string
		want                             auditv1.Level
	}{
		{"create", "", "secrets", "Metadata", auditv1.LevelMetadata},
		{"create", "", "secrets", "RequestResponse", auditv1.LevelMetadata},
		{"get", "", "secrets", "RequestResponse", auditv1.LevelMetadata},
		{"create", "", "configmaps", "RequestResponse", auditv1.LevelMetadata},
		{"create", "authentication.k8s.io", "tokenreviews", "RequestResponse", auditv1.LevelMetadata},
		{"create", bedrock, "settings", "Metadata", auditv1.LevelRequestResponse},
		{"delete", bedrock, "users", "Metadata", auditv1.LevelRequestResponse},
		{"create", bedrock, "credentials", "Metadata", auditv1.LevelMetadata},
		{"create", bedrock, "refreshtokens", "Metadata", auditv1.LevelMetadata},
		{"create", bedrock, "signingkeys", "Metadata", auditv1.LevelMetadata},
		{"update", bedrock, "hosts/status", "Metadata", auditv1.LevelMetadata},
		{"get", bedrock, "settings", "Metadata", auditv1.LevelMetadata},
		{"get", bedrock, "settings", "RequestResponse", auditv1.LevelRequestResponse},
		{"create", "apps", "deployments", "RequestResponse", auditv1.LevelMetadata},
		{"get", "", "events", "Metadata", auditv1.LevelNone},
		{"update", "coordination.k8s.io", "leases", "Metadata", auditv1.LevelNone},
	}
	for _, c := range cases {
		resource, subresource, _ := strings.Cut(c.resource, "/")
		attrs := authorizer.AttributesRecord{Verb: c.verb, APIGroup: c.group, Resource: resource, Subresource: subresource, ResourceRequest: true, User: &user.DefaultInfo{Name: "admin"}}
		if got := evaluator(t, c.readLevel).EvaluatePolicyRule(attrs).Level; got != c.want {
			t.Errorf("%s %s/%s (%s): got %s, want %s", c.verb, c.group, c.resource, c.readLevel, got, c.want)
		}
	}
	health := authorizer.AttributesRecord{Verb: "get", Path: "/healthz", User: &user.DefaultInfo{Name: "admin"}}
	if got := evaluator(t, "Metadata").EvaluatePolicyRule(health).Level; got != auditv1.LevelNone {
		t.Errorf("/healthz: got %s", got)
	}
}

func TestPolicyDropsNodeReads(t *testing.T) {
	node := &user.DefaultInfo{Name: "system:node:a", Groups: []string{"system:nodes"}}
	read := authorizer.AttributesRecord{Verb: "get", APIGroup: "bedrock.cloudyfolks.io", Resource: "hosts", ResourceRequest: true, User: node}
	if got := evaluator(t, "RequestResponse").EvaluatePolicyRule(read).Level; got != auditv1.LevelNone {
		t.Errorf("node read: got %s", got)
	}
}

func TestPolicyOmitsTheRequestReceivedStage(t *testing.T) {
	attrs := authorizer.AttributesRecord{Verb: "create", APIGroup: "apps", Resource: "deployments", ResourceRequest: true, User: &user.DefaultInfo{Name: "admin"}}
	stages := evaluator(t, "Metadata").EvaluatePolicyRule(attrs).OmitStages
	if !slices.Contains(stages, auditv1.StageRequestReceived) {
		t.Errorf("stages: %v", stages)
	}
}

func TestArgsRotateAt100MBKeep10(t *testing.T) {
	want := map[string]string{
		"audit-policy-file":   "/etc/bedrock/audit/policy.yaml",
		"audit-log-path":      "/var/log/bedrock/audit/audit.log",
		"audit-log-maxsize":   "100",
		"audit-log-maxbackup": "10",
	}
	if !maps.Equal(Args(), want) {
		t.Errorf("got %v", Args())
	}
}

func TestPolicyKeepsTheWebhookSettingAtMetadata(t *testing.T) {
	for _, level := range []string{"Metadata", "RequestResponse"} {
		for _, verb := range []string{"get", "list", "patch", "update", "create", "delete"} {
			attrs := authorizer.AttributesRecord{Verb: verb, APIGroup: "bedrock.cloudyfolks.io", Resource: "settings", Name: "alertmanager.webhook-url", ResourceRequest: true, User: &user.DefaultInfo{Name: "admin"}}
			if got := evaluator(t, level).EvaluatePolicyRule(attrs).Level; got != auditv1.LevelMetadata {
				t.Errorf("%s with read level %s: got %s", verb, level, got)
			}
		}
	}
	other := authorizer.AttributesRecord{Verb: "patch", APIGroup: "bedrock.cloudyfolks.io", Resource: "settings", Name: "images.keep", ResourceRequest: true, User: &user.DefaultInfo{Name: "admin"}}
	if got := evaluator(t, "Metadata").EvaluatePolicyRule(other).Level; got != auditv1.LevelRequestResponse {
		t.Errorf("another setting: got %s", got)
	}
}
