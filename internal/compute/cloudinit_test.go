package compute

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestUserDataIsVerbatimWithoutKeys(t *testing.T) {
	for _, in := range []string{"#!/bin/bash\necho hi\n", "#cloud-config\n# keep me\nhostname: web-1\n"} {
		got, err := UserData(in, nil)
		if err != nil || got != in {
			t.Errorf("%q came back as %q, %v", in, got, err)
		}
	}
}

const richUserData = `#cloud-config
# keep this comment
hostname: web-1
write_files:
  - path: /etc/motd
    permissions: 0644
    content: |
      line one
        indented line
      line three
defaults: &defaults
  retries: 3
other:
  <<: *defaults
`

func TestUserDataKeepsTheTenantBytes(t *testing.T) {
	got, err := UserData(richUserData, []string{"ssh-ed25519 AAAAk1", "ssh-ed25519 AAAAk2", "ssh-ed25519 AAAAk1"})
	if err != nil {
		t.Fatal(err)
	}
	want := richUserData + "ssh_authorized_keys:\n- ssh-ed25519 AAAAk1\n- ssh-ed25519 AAAAk2\n"
	if got != want {
		t.Fatalf("user data changed:\n%s", got)
	}
	for _, kept := range []string{"permissions: 0644", "# keep this comment", "    content: |\n      line one\n        indented line", "&defaults", "<<: *defaults"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was lost", kept)
		}
	}
}

func TestUserDataAddsNewlineBeforeKeys(t *testing.T) {
	got, err := UserData("#cloud-config\nhostname: web-1", []string{"ssh-ed25519 AAAAk1"})
	if err != nil || got != "#cloud-config\nhostname: web-1\nssh_authorized_keys:\n- ssh-ed25519 AAAAk1\n" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = UserData("#cloud-config\n# only a comment\n", []string{"ssh-ed25519 AAAAk1"})
	if err != nil || !strings.HasSuffix(got, "ssh_authorized_keys:\n- ssh-ed25519 AAAAk1\n") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestUserDataGeneratesCloudConfigWhenEmpty(t *testing.T) {
	got, err := UserData("", []string{"ssh-ed25519 AAAAk1"})
	if err != nil || got != "#cloud-config\nssh_authorized_keys:\n- ssh-ed25519 AAAAk1\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestUserDataRefusesDocumentsItCannotExtend(t *testing.T) {
	keys := []string{"ssh-ed25519 AAAAk1"}
	cases := map[string]error{
		"#cloud-config\nssh_authorized_keys:\n  - ssh-ed25519 AAAAexisting\n": ErrUserDataSetsKeys,
		"#cloud-config\n{hostname: web-1}\n":                                  ErrUserDataNotMapping,
		"#cloud-config\n- a\n- b\n":                                           ErrUserDataNotMapping,
		"#cloud-config\nhostname: a\n---\nhostname: b\n":                      ErrUserDataNotMapping,
	}
	for in, want := range cases {
		if _, err := UserData(in, keys); !errors.Is(err, want) {
			t.Errorf("%q gave %v, want %v", in, err, want)
		}
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
	if err != nil || got != "#cloud-config\nssh_authorized_keys:\n- "+hostile+"\n" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestUserDataErrorsNeverEchoTheContent(t *testing.T) {
	_, err := UserData("#cloud-config\npassword: topsecret\n  broken: [unclosed topsecret\n", []string{"ssh-ed25519 AAAAk1"})
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(err.Error(), "near line") {
		t.Fatalf("error %v must name the line", err)
	}
}

func TestUserDataRefusesScriptsWithKeys(t *testing.T) {
	for _, in := range []string{"#!/bin/bash\necho hi\n", "Content-Type: multipart/mixed; boundary=x\n"} {
		if _, err := UserData(in, []string{"ssh-ed25519 AAAAk1"}); !errors.Is(err, ErrUserDataNotCloudConfig) {
			t.Errorf("%q: %v", in, err)
		}
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
