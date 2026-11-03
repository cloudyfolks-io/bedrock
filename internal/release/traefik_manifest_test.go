package release

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func traefikObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	bundle, _ := buildReal(t, "traefik")
	for _, group := range bundle.Groups {
		if group.Name == "traefik" {
			return group.Objects
		}
	}
	t.Fatal("group traefik missing")
	return nil
}

func TestTraefikRunsOnControlPlaneNodesAndKeepsTheClientAddress(t *testing.T) {
	objects := traefikObjects(t)
	if objectNamed(objects, "Deployment", "traefik") != nil {
		t.Error("Deployment traefik must not render")
	}
	daemon := objectNamed(objects, "DaemonSet", "traefik")
	if daemon == nil {
		t.Fatal("missing DaemonSet traefik")
	}
	selector, _, _ := unstructured.NestedStringMap(daemon.Object, "spec", "template", "spec", "nodeSelector")
	if selector["bedrock.cloudyfolks.io/role-control-plane"] != "true" {
		t.Errorf("nodeSelector %v", selector)
	}
	service := objectNamed(objects, "Service", "traefik")
	if service == nil {
		t.Fatal("missing Service traefik")
	}
	if policy, _, _ := unstructured.NestedString(service.Object, "spec", "externalTrafficPolicy"); policy != "Local" {
		t.Errorf("externalTrafficPolicy %q", policy)
	}
}

func TestTraefikRendersTheDefaultTLSOption(t *testing.T) {
	option := objectNamed(traefikObjects(t), "TLSOption", "default")
	if option == nil {
		t.Fatal("missing TLSOption default")
	}
	if got, _, _ := unstructured.NestedString(option.Object, "spec", "minVersion"); got != "VersionTLS12" {
		t.Errorf("minVersion %q", got)
	}
	suites, _, _ := unstructured.NestedStringSlice(option.Object, "spec", "cipherSuites")
	if len(suites) != 6 {
		t.Errorf("cipherSuites %v", suites)
	}
	for _, suite := range suites {
		if !strings.HasPrefix(suite, "TLS_ECDHE_") {
			t.Errorf("cipher suite %s", suite)
		}
	}
}
