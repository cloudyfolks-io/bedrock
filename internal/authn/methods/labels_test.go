package methods

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestCredentialsOfLongUsernamesGetValidLabelValues(t *testing.T) {
	c, _ := startTestEnv(t)
	usernames := map[string]string{
		"sixty-two":          strings.Repeat("a", 62),
		"sixty-two-dotted":   strings.Repeat("b", 30) + "." + strings.Repeat("b", 31),
		"sixty-three":        strings.Repeat("c", 63),
		"sixty-three-dotted": strings.Repeat("d", 31) + "." + strings.Repeat("d", 31),
	}
	for label, username := range usernames {
		t.Run(label, func(t *testing.T) {
			user := createUser(t, c, username)
			ctx := context.Background()
			if err := SetPassword(ctx, c, rand.Reader, user, "s3cret-passphrase"); err != nil {
				t.Fatalf("password credential: %v", err)
			}
			if _, err := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test")).Enroll(ctx, user, Answer{}); err != nil {
				t.Fatalf("totp credential: %v", err)
			}
			if _, err := NewRecovery(c, rand.Reader).Enroll(ctx, user, Answer{}); err != nil {
				t.Fatalf("recovery credential: %v", err)
			}
			for _, method := range []string{v1alpha1.MethodPassword, v1alpha1.MethodTOTP, v1alpha1.MethodRecovery} {
				var cred v1alpha1.Credential
				if err := c.Get(ctx, client.ObjectKey{Namespace: "bedrock-system", Name: v1alpha1.CredentialName(user.Name, method)}, &cred); err != nil {
					t.Fatalf("%s credential: %v", method, err)
				}
			}
		})
	}
}
