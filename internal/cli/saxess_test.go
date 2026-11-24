package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/operator"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/saxess"
)

func TestSAxessInvalidArgsDoNotOpenClient(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"disable-operator", "operator", "--state-secret", "card-state"},
		{"invite", "operator"},
		{"invite", "operator", "--state-secret", "other/card-state"},
		{"invite", "operator", "--state-secret", "card-state", "extra"},
		{"invite", "operator", "--state-secret", "card-state", "--device-id", "phone"},
		{"approve-device", "operator", "--state-secret", "card-state", "--device-id", "phone"},
		{"revoke-device", "operator", "--state-secret", "card-state"},
	} {
		open := func(string) (client.Client, error) {
			t.Fatalf("opened a client for invalid arguments %v", args)
			return nil, nil
		}
		if code := runSAxess(context.Background(), args, open, io.Discard, io.Discard); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestSAxessAdminCommands(t *testing.T) {
	ctx := context.Background()
	scheme, err := operator.Scheme()
	if err != nil {
		t.Fatal(err)
	}
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: v1alpha1.UserObjectName("operator"), UID: "original-uid"},
		Spec:       v1alpha1.UserSpec{Email: "operator@example.com", Groups: []string{v1alpha1.GroupAdmins}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: "card-state"},
		Data: map[string][]byte{"state.json": []byte(`{
			"Devices": {
				"phone": {"OperatorID": "original-uid", "PublicKey": "known-key", "TokenDigest": "digest"},
				"old-phone": {"OperatorID": "replaced-uid", "PublicKey": "known-key", "TokenDigest": "old-digest"}
			},
			"Tokens": {"digest": "phone", "old-digest": "old-phone"}
		}`)},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(user, secret).Build()
	run := func(want int, operation, username string, options ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := append([]string{operation, username, "--state-secret", "card-state", "--kubeconfig", "/trusted.conf"}, options...)
		code := runSAxess(ctx, args, func(path string) (client.Client, error) {
			if path != "/trusted.conf" {
				t.Fatalf("kubeconfig %q", path)
			}
			return c, nil
		}, &stdout, &stderr)
		if code != want || (code == 0 && stderr.Len() != 0) || (operation != "invite" || code != 0) && stdout.Len() != 0 {
			t.Fatalf("%s: exit %d, stderr length %d, stdout length %d", operation, code, stderr.Len(), stdout.Len())
		}
		return stdout.String()
	}
	store := saxess.NewSecretStore(c, c, client.ObjectKeyFromObject(secret))
	check := func(fn func(*saxess.State)) {
		t.Helper()
		if err := store.View(ctx, func(state *saxess.State) error { fn(state); return nil }); err != nil {
			t.Fatal(err)
		}
	}

	run(1, "invite", "missing")
	var invite saxess.EnrollmentInvite
	if err := json.Unmarshal([]byte(run(0, "invite", "operator")), &invite); err != nil || invite.Token == "" || invite.Payload == "" || invite.ExpiresAt.IsZero() {
		t.Fatal("stdout must contain only an invite JSON object")
	}
	check(func(state *saxess.State) {
		if len(state.Invites) != 1 || !state.Operators["original-uid"].Active {
			t.Fatal("invite must bind the current Bedrock user")
		}
	})
	run(1, "approve-device", "operator", "--device-id", "phone", "--public-key", "wrong-key")
	run(1, "approve-device", "operator", "--device-id", "old-phone", "--public-key", "known-key")
	run(1, "revoke-device", "operator", "--device-id", "old-phone")
	check(func(state *saxess.State) {
		if state.Devices["phone"].Approved || state.Devices["old-phone"].Approved || state.Devices["old-phone"].Revoked {
			t.Fatal("rejected commands changed a device")
		}
	})
	run(0, "approve-device", "operator", "--device-id", "phone", "--public-key", "known-key")
	check(func(state *saxess.State) {
		if !state.Devices["phone"].Approved {
			t.Fatal("matching device was not approved")
		}
	})

	if err := c.Get(ctx, client.ObjectKeyFromObject(user), user); err != nil {
		t.Fatal(err)
	}
	user.Spec.Disabled = true
	if err := c.Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	run(1, "invite", "operator")
	run(0, "revoke-device", "operator", "--device-id", "phone")
	check(func(state *saxess.State) {
		if !state.Devices["phone"].Revoked || state.Tokens["digest"] != "" {
			t.Fatal("revoking a disabled operator's device must invalidate its token")
		}
	})
}
