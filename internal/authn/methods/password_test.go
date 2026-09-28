package methods

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
)

func createUser(t *testing.T, c client.Client, username string) v1alpha1.User {
	t.Helper()
	name := v1alpha1.UserObjectName(username)
	user := v1alpha1.User{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "User"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name, Labels: map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name}},
		Spec:       v1alpha1.UserSpec{Username: username, Methods: []string{v1alpha1.MethodPassword}},
	}
	if err := c.Create(context.Background(), &user); err != nil {
		t.Fatal(err)
	}
	return user
}

func refetchUser(t *testing.T, c client.Client, user v1alpha1.User) v1alpha1.User {
	t.Helper()
	var fresh v1alpha1.User
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: user.Name}, &fresh); err != nil {
		t.Fatal(err)
	}
	return fresh
}

func fixedSettings(threshold int) func(context.Context) (policy.Settings, error) {
	return func(context.Context) (policy.Settings, error) {
		return policy.Settings{LockoutThreshold: threshold}, nil
	}
}

func TestPasswordLogin(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "alice")
	if err := SetPassword(context.Background(), c, rand.Reader, user, "s3cret-passphrase"); err != nil {
		t.Fatal(err)
	}
	method := NewPassword(c, rand.Reader, NewRateLimiter(10, time.Minute), fixedSettings(5))
	now := time.Now()
	flow := Flow{ClientIP: "10.0.0.1", Now: now}

	wrong, err := method.Complete(context.Background(), flow, user, Answer{Password: "not it"})
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Failure != FailureInvalidCredentials {
		t.Fatalf("wrong password result = %+v", wrong)
	}

	right, err := method.Complete(context.Background(), flow, refetchUser(t, c, user), Answer{Password: "s3cret-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	if right.Subject == nil || right.Subject.User.Name != user.Name || len(right.Subject.AMR) != 1 || right.Subject.AMR[0] != "pwd" {
		t.Fatalf("correct password result = %+v", right)
	}
	if after := refetchUser(t, c, user); after.Status.LastLogin == nil {
		t.Fatal("a successful login must set status.lastLogin")
	}
}

func TestPasswordLockoutAfterThreshold(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "bob")
	if err := SetPassword(context.Background(), c, rand.Reader, user, "s3cret-passphrase"); err != nil {
		t.Fatal(err)
	}
	method := NewPassword(c, rand.Reader, NewRateLimiter(100, time.Minute), fixedSettings(3))
	now := time.Now()
	for i := 0; i < 2; i++ {
		flow := Flow{ClientIP: "10.0.0.2", Now: now.Add(time.Duration(i) * time.Second)}
		result, err := method.Complete(context.Background(), flow, refetchUser(t, c, user), Answer{Password: "wrong"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Failure != FailureInvalidCredentials {
			t.Fatalf("attempt %d = %+v", i, result)
		}
	}
	third, err := method.Complete(context.Background(), Flow{ClientIP: "10.0.0.2", Now: now.Add(2 * time.Second)}, refetchUser(t, c, user), Answer{Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if third.Failure != FailureInvalidCredentials {
		t.Fatalf("the 3rd wrong attempt itself is still invalid_credentials: %+v", third)
	}
	locked, err := method.Complete(context.Background(), Flow{ClientIP: "10.0.0.2", Now: now.Add(3 * time.Second)}, refetchUser(t, c, user), Answer{Password: "s3cret-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	if locked.Failure != FailureLocked {
		t.Fatalf("a locked user must refuse even the right password: %+v", locked)
	}
}

func TestPasswordLockoutIgnoresCase(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "carol")
	if err := SetPassword(context.Background(), c, rand.Reader, user, "s3cret-passphrase"); err != nil {
		t.Fatal(err)
	}
	method := NewPassword(c, rand.Reader, NewRateLimiter(100, time.Minute), fixedSettings(3))
	now := time.Now()
	for i, raw := range []string{"Carol", " carol ", "CAROL"} {
		normalized, err := v1alpha1.NormalizeUsername(raw)
		if err != nil {
			t.Fatal(err)
		}
		if normalized != "carol" {
			t.Fatalf("NormalizeUsername(%q) = %q, want carol", raw, normalized)
		}
		flow := Flow{ClientIP: "10.0.0.3", Now: now.Add(time.Duration(i) * time.Second)}
		if _, err := method.Complete(context.Background(), flow, refetchUser(t, c, user), Answer{Password: "wrong"}); err != nil {
			t.Fatal(err)
		}
	}
	final := refetchUser(t, c, user)
	if final.Status.Locks != 1 || final.Status.LockedUntil == nil {
		t.Fatalf("three failures against three casings of the same username must share one lockout counter: %+v", final.Status)
	}
}

func TestPasswordUnknownUserSameFailure(t *testing.T) {
	c, _ := startTestEnv(t)
	method := NewPassword(c, rand.Reader, NewRateLimiter(100, time.Minute), fixedSettings(5))
	flow := Flow{ClientIP: "10.0.0.4", Now: time.Now()}
	result, err := method.Complete(context.Background(), flow, v1alpha1.User{}, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureInvalidCredentials {
		t.Fatalf("an unknown user must fail the same way as a wrong password: %+v", result)
	}
}
