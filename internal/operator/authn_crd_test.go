package operator

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestIdentityKindsValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
		Spec:       v1alpha1.UserSpec{Username: "alice", Methods: []string{v1alpha1.MethodPassword}},
	}
	if err := c.Create(ctx, user); err != nil {
		t.Fatal(err)
	}
	user.Spec.Username = "bob"
	if err := c.Update(ctx, user); err == nil {
		t.Fatal("username must be immutable")
	}

	group := &v1alpha1.Group{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.GroupAdmins, Namespace: "default"}, Spec: v1alpha1.GroupSpec{Members: []string{"alice"}}}
	if err := c.Create(ctx, group); err != nil {
		t.Fatal(err)
	}

	cred := &v1alpha1.Credential{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CredentialName("alice", v1alpha1.MethodPassword), Namespace: "default"},
		Spec:       v1alpha1.CredentialSpec{UserRef: "alice", Method: v1alpha1.MethodPassword, SecretRef: "alice-password"},
	}
	if err := c.Create(ctx, cred); err != nil {
		t.Fatal(err)
	}
	cred.Spec.UserRef = "bob"
	if err := c.Update(ctx, cred); err == nil {
		t.Fatal("userRef must be immutable")
	}
	var freshCred v1alpha1.Credential
	if err := c.Get(ctx, client.ObjectKeyFromObject(cred), &freshCred); err != nil {
		t.Fatal(err)
	}
	freshCred.Spec.Method = v1alpha1.MethodTOTP
	if err := c.Update(ctx, &freshCred); err == nil {
		t.Fatal("method must be immutable")
	}

	var list v1alpha1.CredentialList
	selector := client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.userRef", "alice")}
	if err := c.List(ctx, &list, selector); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Spec.UserRef != "alice" {
		t.Fatalf("field selector spec.userRef=alice returned %+v", list.Items)
	}

	ldapIDP := &v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "corp-ldap", Namespace: "default"},
		Spec: v1alpha1.IdentityProviderSpec{
			Type: v1alpha1.IdentityProviderTypeLDAP,
			LDAP: &v1alpha1.LDAPProvider{URL: "ldaps://ldap.example.com", BindDN: "cn=svc", UserSearch: v1alpha1.LDAPUserSearch{BaseDN: "ou=people", UsernameAttribute: "uid"}},
		},
	}
	if err := c.Create(ctx, ldapIDP); err != nil {
		t.Fatal(err)
	}
	ldapIDP.Spec.Type = v1alpha1.IdentityProviderTypeOIDC
	if err := c.Update(ctx, ldapIDP); err == nil {
		t.Fatal("type is immutable")
	}

	oidcMissing := &v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-oidc", Namespace: "default"},
		Spec:       v1alpha1.IdentityProviderSpec{Type: v1alpha1.IdentityProviderTypeOIDC},
	}
	if err := c.Create(ctx, oidcMissing); err == nil {
		t.Fatal("oidc must be set when type is oidc")
	}
	ldapWithOIDC := &v1alpha1.IdentityProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-ldap", Namespace: "default"},
		Spec: v1alpha1.IdentityProviderSpec{
			Type: v1alpha1.IdentityProviderTypeLDAP,
			LDAP: &v1alpha1.LDAPProvider{URL: "ldaps://x", BindDN: "cn=svc", UserSearch: v1alpha1.LDAPUserSearch{BaseDN: "ou=people", UsernameAttribute: "uid"}},
			OIDC: &v1alpha1.OIDCProvider{Issuer: "https://x", ClientID: "x"},
		},
	}
	if err := c.Create(ctx, ldapWithOIDC); err == nil {
		t.Fatal("oidc must be unset when type is ldap")
	}

	confidential := &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-console", Namespace: "default"},
		Spec:       v1alpha1.OAuthClientSpec{ClientID: "bedrock-console", SecretRef: "bedrock-console-secret"},
	}
	if err := c.Create(ctx, confidential); err != nil {
		t.Fatal(err)
	}
	confidential.Spec.ClientID = "renamed"
	if err := c.Update(ctx, confidential); err == nil {
		t.Fatal("clientID is immutable")
	}
	publicWithSecret := &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-public", Namespace: "default"},
		Spec:       v1alpha1.OAuthClientSpec{ClientID: "bad-public", Public: true, SecretRef: "should-not-exist"},
	}
	if err := c.Create(ctx, publicWithSecret); err == nil {
		t.Fatal("a public client must not have a secretRef")
	}
	confidentialNoSecret := &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-confidential", Namespace: "default"},
		Spec:       v1alpha1.OAuthClientSpec{ClientID: "bad-confidential"},
	}
	if err := c.Create(ctx, confidentialNoSecret); err == nil {
		t.Fatal("a confidential client requires a secretRef")
	}

	token := &v1alpha1.APIToken{
		ObjectMeta: metav1.ObjectMeta{Name: "deadbeef", Namespace: "default"},
		Spec:       v1alpha1.APITokenSpec{UserRef: "alice"},
	}
	if err := c.Create(ctx, token); err != nil {
		t.Fatal(err)
	}
	token.Spec.UserRef = "bob"
	if err := c.Update(ctx, token); err == nil {
		t.Fatal("APIToken userRef is immutable")
	}
	var tokens v1alpha1.APITokenList
	if err := c.List(ctx, &tokens, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.userRef", "alice")}); err != nil {
		t.Fatal(err)
	}
	if len(tokens.Items) != 1 {
		t.Fatalf("field selector spec.userRef=alice on APIToken returned %+v", tokens.Items)
	}
}

func TestSigningKeyName(t *testing.T) {
	notBefore := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	if got := v1alpha1.SigningKeyName(notBefore); got != "k-20260928t120000z" {
		t.Fatalf("SigningKeyName = %q", got)
	}
}

func TestProtocolKindsValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	expiresAt := metav1.NewTime(time.Now().Add(30 * time.Minute))

	authRequest := &v1alpha1.AuthRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaaaaaaaaaaaaaaaa", Namespace: "default"},
		Spec:       v1alpha1.AuthRequestSpec{ClientID: "bedrock-cli", ResponseType: "code", ExpiresAt: expiresAt},
	}
	if err := c.Create(ctx, authRequest); err != nil {
		t.Fatal(err)
	}
	authRequest.Status.Subject = "alice"
	authRequest.Status.Done = true
	if err := c.Status().Update(ctx, authRequest); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	authRequest.Spec.ClientID = "other"
	if err := c.Update(ctx, authRequest); err == nil {
		t.Fatal("the whole spec must be immutable after create")
	}

	authCode := &v1alpha1.AuthCode{
		ObjectMeta: metav1.ObjectMeta{Name: "deadbeef", Namespace: "default"},
		Spec:       v1alpha1.AuthCodeSpec{AuthRequest: authRequest.Name, ExpiresAt: expiresAt},
	}
	if err := c.Create(ctx, authCode); err != nil {
		t.Fatal(err)
	}
	authCode.Spec.AuthRequest = "other"
	if err := c.Update(ctx, authCode); err == nil {
		t.Fatal("AuthCode spec must be immutable")
	}

	refresh := &v1alpha1.RefreshToken{
		ObjectMeta: metav1.ObjectMeta{Name: "refreshhash", Namespace: "default"},
		Spec: v1alpha1.RefreshTokenSpec{
			Family: "family-1", UserRef: "alice", ClientID: "bedrock-cli",
			AuthTime: metav1.NewTime(time.Now()), ExpiresAt: metav1.NewTime(time.Now().Add(720 * time.Hour)),
		},
	}
	if err := c.Create(ctx, refresh); err != nil {
		t.Fatal(err)
	}
	refresh.Spec.Family = "family-2"
	if err := c.Update(ctx, refresh); err == nil {
		t.Fatal("RefreshToken spec must be immutable")
	}
	var byFamily v1alpha1.RefreshTokenList
	if err := c.List(ctx, &byFamily, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.family", "family-1")}); err != nil {
		t.Fatal(err)
	}
	if len(byFamily.Items) != 1 {
		t.Fatalf("field selector spec.family=family-1 returned %+v", byFamily.Items)
	}
	var byUser v1alpha1.RefreshTokenList
	if err := c.List(ctx, &byUser, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.userRef", "alice")}); err != nil {
		t.Fatal(err)
	}
	if len(byUser.Items) != 1 {
		t.Fatalf("field selector spec.userRef=alice returned %+v", byUser.Items)
	}

	session := &v1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "sessionhash", Namespace: "default"},
		Spec: v1alpha1.SessionSpec{
			UserRef: "alice", AuthTime: metav1.NewTime(time.Now()), ExpiresAt: metav1.NewTime(time.Now().Add(12 * time.Hour)),
		},
	}
	if err := c.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	var sessionsByUser v1alpha1.SessionList
	if err := c.List(ctx, &sessionsByUser, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.userRef", "alice")}); err != nil {
		t.Fatal(err)
	}
	if len(sessionsByUser.Items) != 1 {
		t.Fatalf("field selector spec.userRef=alice on Session returned %+v", sessionsByUser.Items)
	}

	device := &v1alpha1.DeviceRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "devicehash", Namespace: "default"},
		Spec:       v1alpha1.DeviceRequestSpec{ClientID: "bedrock-cli", UserCodeHash: "codehash", ExpiresAt: expiresAt},
	}
	if err := c.Create(ctx, device); err != nil {
		t.Fatal(err)
	}
	device.Status.State = v1alpha1.DeviceStateApproved
	if err := c.Status().Update(ctx, device); err != nil {
		t.Fatalf("DeviceRequest status must be writable: %v", err)
	}
	var byUserCode v1alpha1.DeviceRequestList
	if err := c.List(ctx, &byUserCode, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.userCodeHash", "codehash")}); err != nil {
		t.Fatal(err)
	}
	if len(byUserCode.Items) != 1 {
		t.Fatalf("field selector spec.userCodeHash=codehash returned %+v", byUserCode.Items)
	}

	notBefore := metav1.NewTime(time.Now())
	signingKey := &v1alpha1.SigningKey{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.SigningKeyName(notBefore.Time), Namespace: "default"},
		Spec: v1alpha1.SigningKeySpec{
			Algorithm: v1alpha1.AlgorithmES256, SecretRef: v1alpha1.SigningKeyName(notBefore.Time),
			NotBefore: notBefore, RetireAfter: metav1.NewTime(notBefore.Add(30 * 24 * time.Hour)),
		},
	}
	if err := c.Create(ctx, signingKey); err != nil {
		t.Fatal(err)
	}
	signingKey.Spec.SecretRef = "other"
	if err := c.Update(ctx, signingKey); err == nil {
		t.Fatal("SigningKey spec must be immutable")
	}
}
