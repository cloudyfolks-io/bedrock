package release

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAuthnDeploymentImageIsRewrittenLikeTheOperator(t *testing.T) {
	image := "ghcr.io/cloudyfolks-io/bedrock:v9.9.9"
	for _, file := range []string{filepath.Join("85-authn", "30-deployment.yaml"), filepath.Join("90-bedrock", "operator.yaml")} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", file))
		if err != nil {
			t.Fatal(err)
		}
		objects, err := splitDocuments(raw)
		if err != nil {
			t.Fatal(err)
		}
		rewritten := rewriteBedrockImage([]rendered{{group: filepath.Dir(file), file: filepath.Base(file), objects: objects}}, image)
		found := false
		for _, obj := range rewritten[0].objects {
			if obj.GetKind() != "Deployment" {
				continue
			}
			containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
			if err != nil || len(containers) != 1 {
				t.Fatalf("%s: containers %v, %v", file, containers, err)
			}
			container, _ := containers[0].(map[string]any)
			if container["image"] != image {
				t.Fatalf("%s: image %v, want %s", file, container["image"], image)
			}
			found = true
		}
		if !found {
			t.Fatalf("%s holds no Deployment", file)
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "85-authn", "30-deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := splitDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(objects[0].Object, "spec", "template", "spec", "containers")
	container, _ := containers[0].(map[string]any)
	if !reflect.DeepEqual(container["args"], []any{"authn", "serve"}) {
		t.Fatalf("args %v", container["args"])
	}
	replicas, _, _ := unstructured.NestedInt64(objects[0].Object, "spec", "replicas")
	if replicas != 2 {
		t.Fatalf("replicas %d", replicas)
	}
}

func authnManifest(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "85-authn", file))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := splitDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func TestAuthnRoleGrantsTheCookieKeySecretAccess(t *testing.T) {
	objects := authnManifest(t, "10-rbac.yaml")
	for _, obj := range objects {
		if obj.GetKind() != "Role" || obj.GetName() != "bedrock-authn" {
			continue
		}
		rules, _, _ := unstructured.NestedSlice(obj.Object, "rules")
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
			if !containsString(resources, "secrets") {
				continue
			}
			verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
			if containsString(verbs, "create") && containsString(verbs, "get") {
				return
			}
		}
	}
	t.Fatal("Role bedrock-authn must allow create and get on Secrets so server.New can write and read the cookie key")
}

func TestAuthnSecretPolicyAcceptsTheAuthnLabel(t *testing.T) {
	objects := authnManifest(t, "20-secret-policy.yaml")
	for _, obj := range objects {
		if obj.GetKind() != "ValidatingAdmissionPolicy" {
			continue
		}
		validations, _, _ := unstructured.NestedSlice(obj.Object, "spec", "validations")
		for _, v := range validations {
			validation, _ := v.(map[string]any)
			expression, _ := validation["expression"].(string)
			if strings.Contains(expression, "bedrock.cloudyfolks.io/authn") {
				return
			}
		}
	}
	t.Fatal("the secret ValidatingAdmissionPolicy must require the bedrock.cloudyfolks.io/authn label bedrock-authn writes on its cookie key")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
