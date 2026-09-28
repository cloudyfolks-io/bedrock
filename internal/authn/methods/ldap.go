package methods

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-ldap/ldap/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func UserFilter(search v1alpha1.LDAPUserSearch, username string) string {
	filter := search.Filter
	if filter == "" {
		filter = "(objectClass=person)"
	}
	return fmt.Sprintf("(&%s(%s=%s))", filter, search.UsernameAttribute, ldap.EscapeFilter(username))
}

func GroupFilter(search v1alpha1.LDAPGroupSearch, userDN string) string {
	filter := search.Filter
	if filter == "" {
		filter = "(objectClass=groupOfNames)"
	}
	return fmt.Sprintf("(&%s(%s=%s))", filter, search.MemberAttribute, ldap.EscapeFilter(userDN))
}

func appendMethod(methods []string, method string) []string {
	for _, m := range methods {
		if m == method {
			return methods
		}
	}
	return append(append([]string{}, methods...), method)
}

func providerSecrets(ctx context.Context, c client.Client, provider v1alpha1.IdentityProvider) ([]byte, string, error) {
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: provider.Spec.SecretRef}, &sec); err != nil {
		return nil, "", err
	}
	return []byte(provider.Spec.CABundle), string(sec.Data["bindPassword"]), nil
}

func commonName(dn string) (string, bool) {
	parsed, err := ldap.ParseDN(dn)
	if err != nil || len(parsed.RDNs) == 0 {
		return "", false
	}
	for _, attr := range parsed.RDNs[0].Attributes {
		if strings.EqualFold(attr.Type, "CN") {
			return attr.Value, true
		}
	}
	return "", false
}

func ldapGroupsFor(conn LDAPConn, provider v1alpha1.IdentityProvider, entry *ldap.Entry) ([]string, error) {
	if provider.Spec.LDAP.MemberOfAttribute != "" {
		names := make([]string, 0)
		for _, dn := range entry.GetAttributeValues(provider.Spec.LDAP.MemberOfAttribute) {
			if cn, ok := commonName(dn); ok {
				names = append(names, cn)
			}
		}
		return names, nil
	}
	if provider.Spec.LDAP.GroupSearch == nil {
		return nil, nil
	}
	result, err := conn.Search(ldap.NewSearchRequest(
		provider.Spec.LDAP.GroupSearch.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		GroupFilter(*provider.Spec.LDAP.GroupSearch, entry.DN), []string{provider.Spec.LDAP.GroupSearch.NameAttribute}, nil,
	))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(result.Entries))
	for _, e := range result.Entries {
		names = append(names, e.GetAttributeValue(provider.Spec.LDAP.GroupSearch.NameAttribute))
	}
	return names, nil
}

type ldapMethod struct {
	client client.Client
	dial   LDAPDialer
}

func NewLDAP(c client.Client, dial LDAPDialer) Method {
	return ldapMethod{client: c, dial: dial}
}

func (m ldapMethod) Name() string { return v1alpha1.MethodLDAP }

func (m ldapMethod) Kind() Kind { return Primary }

func (m ldapMethod) Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error) {
	return Challenge{Type: ChallengePassword, Username: flow.AuthRequest.Status.Login.Username}, nil
}

func (m ldapMethod) Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error) {
	if answer.Password == "" {
		return Result{Failure: FailureInvalidCredentials}, nil
	}
	providerName := flow.AuthRequest.Status.Login.Provider
	var provider v1alpha1.IdentityProvider
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: providerName}, &provider); err != nil {
		return Result{Failure: FailureProviderError}, nil
	}
	identity, ok, err := m.authenticate(ctx, provider, flow.AuthRequest.Status.Login.Username, answer.Password)
	if err != nil {
		return Result{Failure: FailureProviderError}, nil
	}
	if !ok {
		return Result{Failure: FailureInvalidCredentials}, nil
	}
	var existing *v1alpha1.User
	if user.Name != "" {
		existing = &user
	}
	if existing != nil && existing.Spec.Source != providerName {
		return Result{Failure: FailureInvalidCredentials}, nil
	}
	provisioned, err := m.provision(ctx, existing, providerName, identity)
	if err != nil {
		return Result{}, err
	}
	return Result{Subject: &Subject{User: provisioned, AMR: []string{"pwd"}}}, nil
}

func (m ldapMethod) provision(ctx context.Context, existing *v1alpha1.User, providerName string, identity ExternalIdentity) (v1alpha1.User, error) {
	if existing == nil {
		created := ProvisionUser(nil, providerName, identity)
		created.Spec.Methods = appendMethod(created.Spec.Methods, v1alpha1.MethodLDAP)
		if err := m.client.Create(ctx, &created, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return v1alpha1.User{}, err
		}
		return created, nil
	}
	var updated v1alpha1.User
	err := retry.RetryOnConflict(conflictRetryBackoff, func() error {
		var fresh v1alpha1.User
		if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: existing.Name}, &fresh); err != nil {
			return err
		}
		next := ProvisionUser(&fresh, providerName, identity)
		next.Spec.Methods = appendMethod(next.Spec.Methods, v1alpha1.MethodLDAP)
		if err := m.client.Update(ctx, &next, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return err
		}
		updated = next
		return nil
	})
	if err != nil {
		return v1alpha1.User{}, err
	}
	return updated, nil
}

func (m ldapMethod) Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error) {
	return Enrollment{}, errors.New("methods: ldap has no enrollment step")
}

func (m ldapMethod) authenticate(ctx context.Context, provider v1alpha1.IdentityProvider, username, password string) (ExternalIdentity, bool, error) {
	caBundle, bindPassword, err := providerSecrets(ctx, m.client, provider)
	if err != nil {
		return ExternalIdentity{}, false, err
	}
	conn, err := m.dial(ctx, provider, caBundle)
	if err != nil {
		return ExternalIdentity{}, false, err
	}
	defer conn.Close()
	if err := conn.Bind(provider.Spec.LDAP.BindDN, bindPassword); err != nil {
		return ExternalIdentity{}, false, err
	}
	result, err := conn.Search(ldap.NewSearchRequest(
		provider.Spec.LDAP.UserSearch.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		UserFilter(provider.Spec.LDAP.UserSearch, username), nil, nil,
	))
	if err != nil {
		return ExternalIdentity{}, false, err
	}
	if len(result.Entries) != 1 {
		return ExternalIdentity{}, false, nil
	}
	entry := result.Entries[0]
	if err := conn.Bind(entry.DN, password); err != nil {
		return ExternalIdentity{}, false, nil
	}
	groups, err := ldapGroupsFor(conn, provider, entry)
	if err != nil {
		return ExternalIdentity{}, false, err
	}
	normalized, err := v1alpha1.NormalizeUsername(entry.GetAttributeValue(provider.Spec.LDAP.UserSearch.UsernameAttribute))
	if err != nil {
		normalized = username
	}
	return ExternalIdentity{
		Username:    normalized,
		DisplayName: entry.GetAttributeValue(provider.Spec.LDAP.UserSearch.DisplayNameAttribute),
		Email:       entry.GetAttributeValue(provider.Spec.LDAP.UserSearch.EmailAttribute),
		Groups:      policy.MapGroups(provider.Spec.GroupMapping, groups),
	}, true, nil
}

func LDAPProviderFor(ctx context.Context, c client.Client, dial LDAPDialer, username string) (string, bool, error) {
	var providers v1alpha1.IdentityProviderList
	if err := c.List(ctx, &providers, client.InNamespace(release.SystemNamespace)); err != nil {
		return "", false, err
	}
	sort.Slice(providers.Items, func(i, j int) bool { return providers.Items[i].Name < providers.Items[j].Name })
	for _, provider := range providers.Items {
		if provider.Spec.Type != v1alpha1.MethodLDAP || provider.Spec.Disabled {
			continue
		}
		if matched, err := ldapHasOneEntry(ctx, c, dial, provider, username); err == nil && matched {
			return provider.Name, true, nil
		}
	}
	return "", false, nil
}

func ldapHasOneEntry(ctx context.Context, c client.Client, dial LDAPDialer, provider v1alpha1.IdentityProvider, username string) (bool, error) {
	caBundle, bindPassword, err := providerSecrets(ctx, c, provider)
	if err != nil {
		return false, err
	}
	conn, err := dial(ctx, provider, caBundle)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if err := conn.Bind(provider.Spec.LDAP.BindDN, bindPassword); err != nil {
		return false, err
	}
	result, err := conn.Search(ldap.NewSearchRequest(
		provider.Spec.LDAP.UserSearch.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		UserFilter(provider.Spec.LDAP.UserSearch, username), nil, nil,
	))
	if err != nil {
		return false, err
	}
	return len(result.Entries) == 1, nil
}
