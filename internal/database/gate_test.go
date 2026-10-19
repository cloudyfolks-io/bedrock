package database

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	gateUID  = "0b0c0d0e-1111-4222-8333-444455556666"
	otherUID = "99999999-1111-4222-8333-444455556666"
	haproxy  = "docker.io/library/haproxy:3.2-alpine@sha256:254e6370f01cd654e0744edf607c67f33777c1b6a9dc0d4b28bcba7171460122"
)

func goldenDatabase() v1alpha1.Database {
	return v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "acme-web", UID: gateUID},
		Spec:       newSpec(),
	}
}

func goldenProject() Project { return Project{Namespace: "acme-web", Tenant: "acme", Name: "web"} }

func TestGateConfigGolden(t *testing.T) {
	config, err := GateConfig(GateSpec{ListenPort: 5432, BackendHost: BackendHost(gateUID), BackendPort: 5432, ClientCN: gateUID})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "gate_config", []byte(config))
}

func TestGateConfigRejectsAnyClientWithAnotherName(t *testing.T) {
	one, _ := GateConfig(GateSpec{ListenPort: 5432, BackendHost: "db.local", BackendPort: 5432, ClientCN: gateUID})
	other, _ := GateConfig(GateSpec{ListenPort: 5432, BackendHost: "db.local", BackendPort: 5432, ClientCN: otherUID})
	rule := "tcp-request session reject unless { ssl_c_s_dn(CN) -m str "
	if !strings.Contains(one, rule+gateUID+" }") || strings.Contains(one, otherUID) || !strings.Contains(other, rule+otherUID+" }") {
		t.Fatalf("the CN rule is wrong:\n%s\n%s", one, other)
	}
	if !strings.Contains(one, "verify required") || !strings.Contains(one, "ssl-min-ver TLSv1.2") {
		t.Fatalf("the gate does not demand a client certificate:\n%s", one)
	}
}

func TestGateConfigRefusesBadInput(t *testing.T) {
	good := GateSpec{ListenPort: 5432, BackendHost: "db.local", BackendPort: 5432, ClientCN: gateUID}
	for name, edit := range map[string]func(*GateSpec){
		"CN is not a UID":  func(s *GateSpec) { s.ClientCN = "orders" },
		"CN has a newline": func(s *GateSpec) { s.ClientCN = gateUID + "\nbind :1" },
		"host has a space": func(s *GateSpec) { s.BackendHost = "a b" },
		"empty host":       func(s *GateSpec) { s.BackendHost = "" },
		"zero listen port": func(s *GateSpec) { s.ListenPort = 0 },
		"big backend port": func(s *GateSpec) { s.BackendPort = 70000 },
	} {
		spec := good
		edit(&spec)
		if _, err := GateConfig(spec); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProxyConfigGoldenAndInput(t *testing.T) {
	config, err := ProxyConfig("100.66.16.2", 5432, ServerName(gateUID))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "proxy_config", []byte(config))
	if !strings.Contains(config, "verify required") || !strings.Contains(config, "verifyhost "+gateUID+".dbaas.internal") {
		t.Fatalf("the proxy does not check the gate certificate:\n%s", config)
	}
	for name, call := range map[string]func() error{
		"bad vip":  func() error { _, err := ProxyConfig("not-an-ip", 5432, "a.b"); return err },
		"bad port": func() error { _, err := ProxyConfig("100.66.16.2", 0, "a.b"); return err },
		"bad name": func() error { _, err := ProxyConfig("100.66.16.2", 5432, "A b\n"); return err },
	} {
		if call() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestEndpointImageComesFromTheReleaseList(t *testing.T) {
	images := []string{"quay.io/x/y:1", haproxy, "docker.io/library/haproxy-other:1"}
	if image, found := EndpointImage(images); !found || image != haproxy {
		t.Fatalf("image %q %v", image, found)
	}
	if image, found := EndpointImage([]string{"docker.io/library/haproxy:3.2-alpine"}); !found || image != "docker.io/library/haproxy:3.2-alpine" {
		t.Fatalf("tag only: %q %v", image, found)
	}
	if _, found := EndpointImage([]string{"quay.io/x/y:1"}); found {
		t.Fatal("found an image that is not there")
	}
}

func TestGateObjectsGolden(t *testing.T) {
	objects, err := GateObjects(GateInput{Database: goldenDatabase(), Project: goldenProject(), Image: haproxy, Port: 5432, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	goldenList(t, "gate_objects", objects)
}

func objectOfKind(objects []*unstructured.Unstructured, kind string) *unstructured.Unstructured {
	for _, obj := range objects {
		if obj.GetKind() == kind {
			return obj
		}
	}
	return nil
}

func TestGateCertificatesCarryTheDatabaseNameAndTheRightUsages(t *testing.T) {
	objects, _ := GateObjects(GateInput{Database: goldenDatabase(), Project: goldenProject(), Image: haproxy, Port: 5432})
	server := objectOfKind(objects, "Certificate")
	spec := server.Object["spec"].(map[string]any)
	if spec["commonName"] != gateUID || spec["secretName"] != gateUID+"-server-tls" {
		t.Fatalf("server certificate %v", spec)
	}
	issuer := spec["issuerRef"].(map[string]any)
	if issuer["kind"] != "ClusterIssuer" || issuer["name"] != "bedrock-database-ca" {
		t.Fatalf("issuer %v", issuer)
	}
	names := spec["dnsNames"].([]any)
	if names[0] != gateUID+".dbaas.internal" {
		t.Fatalf("dns names %v", names)
	}
	if usages := spec["usages"].([]any); usages[len(usages)-1] != "server auth" {
		t.Fatalf("usages %v", usages)
	}
	if formats := spec["additionalOutputFormats"].([]any); formats[0].(map[string]any)["type"] != "CombinedPEM" {
		t.Fatalf("formats %v", formats)
	}
	if server.GetNamespace() != PlatformNamespace {
		t.Fatalf("namespace %s", server.GetNamespace())
	}
}

func TestGateObjectsAreAdoptedAndHaveNoOwnerReference(t *testing.T) {
	objects, _ := GateObjects(GateInput{Database: goldenDatabase(), Project: goldenProject(), Image: haproxy, Port: 5432})
	if len(objects) != 4 {
		t.Fatalf("objects %d", len(objects))
	}
	for _, obj := range objects {
		labels := obj.GetLabels()
		if labels[v1alpha1.LabelKind] != Kind || labels[LabelInstance] != gateUID || labels[LabelManagedBy] != ManagedBy || labels[v1alpha1.LabelTenant] != "acme" {
			t.Errorf("%s labels %v", obj.GetKind(), labels)
		}
		if len(obj.GetOwnerReferences()) != 0 || obj.GetAnnotations()["bedrock.cloudyfolks.io/owner-uid"] != gateUID {
			t.Errorf("%s owner %v %v", obj.GetKind(), obj.GetOwnerReferences(), obj.GetAnnotations())
		}
	}
}

func TestGateRolloutHashFollowsTheConfigAndTheCertificateRevision(t *testing.T) {
	hashOf := func(revision int64) string {
		objects, _ := GateObjects(GateInput{Database: goldenDatabase(), Project: goldenProject(), Image: haproxy, Port: 5432, Revision: revision})
		hash, _, _ := unstructured.NestedString(objectOfKind(objects, "Deployment").Object, "spec", "template", "metadata", "annotations", annotationConfigHash)
		return hash
	}
	if hashOf(1) == "" || hashOf(1) != hashOf(1) || hashOf(1) == hashOf(2) {
		t.Fatal("the rollout hash is not stable or does not change")
	}
}

func TestEnginePolicyGolden(t *testing.T) {
	policy, err := EnginePolicyObject(goldenDatabase())
	if err != nil {
		t.Fatal(err)
	}
	goldenList(t, "engine_policy", []*unstructured.Unstructured{policy})
}

func TestGateObjectsRefuseWithoutAnImage(t *testing.T) {
	if _, err := GateObjects(GateInput{Database: goldenDatabase(), Project: goldenProject(), Port: 5432}); err == nil {
		t.Fatal("a gate without an image was rendered")
	}
}
