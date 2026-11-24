package saxess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBedrockInviteBindsImmutableUser(t *testing.T) {
	ctx := context.Background()
	backend, state, _ := newTestBackend()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: "operator", UID: "first-uid"},
		Spec:       v1alpha1.UserSpec{Email: "operator@example.com", Groups: []string{v1alpha1.GroupAdmins}},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(user).Build()
	invite, err := backend.CreateBedrockInvite(ctx, reader, client.ObjectKeyFromObject(user), time.Minute)
	if err != nil || state.state.Invites[credentialDigest(invite.Token)].OperatorID != "first-uid" {
		t.Fatalf("invite did not bind the existing user UID: %v", err)
	}
	user.UID, user.ResourceVersion = "second-uid", ""
	reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(user).Build()
	if _, err := backend.CreateChallenge(ctx, "second-uid", "browser", time.Minute); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("recreated user inherited enrollment: %v", err)
	}
	if err := backend.DisableOperator(ctx, "first-uid"); err != nil {
		t.Fatal(err)
	}
	invite, err = backend.CreateBedrockInvite(ctx, reader, client.ObjectKeyFromObject(user), time.Minute)
	if err != nil || state.state.Invites[credentialDigest(invite.Token)].OperatorID != "second-uid" {
		t.Fatalf("explicit reenrollment did not bind the new UID: %v", err)
	}
	user.Spec.Disabled = true
	if err := reader.Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CreateBedrockInvite(ctx, reader, client.ObjectKeyFromObject(user), time.Minute); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("disabled user could enroll: %v", err)
	}
	user.Spec.Disabled, user.Spec.Groups = false, nil
	if err := reader.Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CreateBedrockInvite(ctx, reader, client.ObjectKeyFromObject(user), time.Minute); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("non-admin user could enroll: %v", err)
	}
}
