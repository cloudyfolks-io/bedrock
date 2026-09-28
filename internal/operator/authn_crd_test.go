package operator

import (
	"context"
	"testing"

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
