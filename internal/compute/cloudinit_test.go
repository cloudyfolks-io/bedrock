package compute

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func parsedCloudConfig(t *testing.T, text string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(text, "#cloud-config\n") {
		t.Fatalf("no header: %q", text)
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUserDataIsVerbatimWithoutKeys(t *testing.T) {
	for _, in := range []string{"#!/bin/bash\necho hi\n", "#cloud-config\n# keep me\nhostname: web-1\n"} {
		got, err := UserData(in, nil)
		if err != nil || got != in {
			t.Errorf("%q came back as %q, %v", in, got, err)
		}
	}
}

func TestUserDataAddsKeysToCloudConfig(t *testing.T) {
	in := "#cloud-config\nhostname: web-1\nssh_authorized_keys:\n  - ssh-ed25519 AAAAexisting\n"
	got, err := UserData(in, []string{"ssh-ed25519 AAAAk1", "ssh-ed25519 AAAAk2", "ssh-ed25519 AAAAk1"})
	if err != nil {
		t.Fatal(err)
	}
	document := parsedCloudConfig(t, got)
	want := []any{"ssh-ed25519 AAAAexisting", "ssh-ed25519 AAAAk1", "ssh-ed25519 AAAAk2"}
	if document["hostname"] != "web-1" || !reflect.DeepEqual(document["ssh_authorized_keys"], want) {
		t.Fatalf("document %+v", document)
	}
}

func TestUserDataGeneratesCloudConfigWhenEmpty(t *testing.T) {
	got, err := UserData("", []string{"ssh-ed25519 AAAAk1"})
	if err != nil || got != "#cloud-config\nssh_authorized_keys:\n- ssh-ed25519 AAAAk1\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestUserDataKeysCannotInjectYAML(t *testing.T) {
	for _, key := range []string{"ssh-ed25519 AAAA\nruncmd:\n- rm -rf /", "ssh-ed25519 AAAA: x"} {
		if _, err := UserData("", []string{key}); err == nil {
			t.Errorf("key %q was accepted", key)
		}
	}
	if _, err := AuthorizedKey(ed25519Key(t) + " x\nruncmd: [x]"); err == nil {
		t.Error("a comment with a newline must be refused")
	}
	hostile, err := AuthorizedKey(ed25519Key(t) + ` x" # y: - z`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UserData("", []string{hostile})
	if err != nil {
		t.Fatal(err)
	}
	document := parsedCloudConfig(t, got)
	if len(document) != 1 || len(document["ssh_authorized_keys"].([]any)) != 1 {
		t.Fatalf("document %+v", document)
	}
}

func TestUserDataRefusesScriptsWithKeys(t *testing.T) {
	for _, in := range []string{"#!/bin/bash\necho hi\n", "Content-Type: multipart/mixed; boundary=x\n"} {
		if _, err := UserData(in, []string{"ssh-ed25519 AAAAk1"}); !errors.Is(err, ErrUserDataNotCloudConfig) {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestUserDataRefusesNonListKeys(t *testing.T) {
	if _, err := UserData("#cloud-config\nssh_authorized_keys: \"x\"\n", []string{"ssh-ed25519 AAAAk1"}); err == nil {
		t.Fatal("a text value must be refused")
	}
}

func TestUserDataRefusesOversizeOutput(t *testing.T) {
	in := "#cloud-config\nwrite_files: \"" + strings.Repeat("a", 65536) + "\"\n"
	if _, err := UserData(in, []string{"ssh-ed25519 AAAAk1"}); err == nil {
		t.Fatal("oversize output must be refused")
	}
}

func TestCloudInitSourcesFromSecret(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{"userdata": []byte("u"), "networkdata": []byte("n")}}
	user, network, err := CloudInitSources(&v1alpha1.VMCloudInit{SecretRef: "s"}, secret)
	if err != nil || user != "u" || network != "n" {
		t.Fatalf("%q %q %v", user, network, err)
	}
	if _, _, err := CloudInitSources(&v1alpha1.VMCloudInit{SecretRef: "s"}, &corev1.Secret{}); err == nil {
		t.Error("a Secret without userdata must be refused")
	}
	user, network, err = CloudInitSources(&v1alpha1.VMCloudInit{UserData: "a", NetworkData: "b"}, nil)
	if err != nil || user != "a" || network != "b" {
		t.Fatalf("%q %q %v", user, network, err)
	}
	if _, _, err := CloudInitSources(&v1alpha1.VMCloudInit{SecretRef: "s", UserData: "a"}, secret); err == nil {
		t.Error("a Secret with inline data must be refused")
	}
	if user, network, err := CloudInitSources(nil, nil); err != nil || user != "" || network != "" {
		t.Errorf("nil gave %q %q %v", user, network, err)
	}
}

func TestCloudInitSecretShape(t *testing.T) {
	vm := v1alpha1.VirtualMachine{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-web", Name: "web-1", UID: types.UID("u1")}}
	labels := map[string]string{"a": "b"}
	secret := CloudInitSecret(vm, labels, "user", "network")
	if secret.Name != "web-1-cloudinit" || secret.Type != corev1.SecretTypeOpaque || string(secret.Data["userdata"]) != "user" || string(secret.Data["networkdata"]) != "network" || !reflect.DeepEqual(secret.Labels, labels) {
		t.Fatalf("secret %+v", secret)
	}
	ref := secret.OwnerReferences[0]
	if ref.APIVersion != "bedrock.cloudyfolks.io/v1alpha1" || ref.Kind != "VirtualMachine" || ref.Controller == nil || !*ref.Controller || ref.UID != "u1" {
		t.Fatalf("owner %+v", ref)
	}
	if _, found := CloudInitSecret(vm, labels, "user", "").Data["networkdata"]; found {
		t.Fatal("empty network data must not be stored")
	}
	labels["a"] = "changed"
	if secret.Labels["a"] != "b" {
		t.Fatal("labels must be copied")
	}
}

func TestOwnerLabelsNeedTenantAndProject(t *testing.T) {
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "web"}}}
	got, ok := OwnerLabels("VirtualMachine", "web-1", ns)
	want := v1alpha1.TenantLabels("VirtualMachine", "web-1", "acme", "web")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, ok)
	}
	ns.Labels = map[string]string{v1alpha1.LabelTenant: "acme"}
	if _, ok := OwnerLabels("VirtualMachine", "web-1", ns); ok {
		t.Fatal("a missing project label must be refused")
	}
}
