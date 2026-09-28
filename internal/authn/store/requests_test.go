package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

type hookClient struct {
	client.Client
	afterGet func()
}

func (h *hookClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := h.Client.Get(ctx, key, obj, opts...)
	if hook := h.afterGet; hook != nil {
		h.afterGet = nil
		hook()
	}
	return err
}

func authorizeRequest(clientID string) *oidc.AuthRequest {
	maxAge := uint(300)
	return &oidc.AuthRequest{
		Scopes:              oidc.SpaceDelimitedArray{oidc.ScopeOpenID, oidc.ScopeOfflineAccess, "groups"},
		ResponseType:        oidc.ResponseTypeCode,
		ClientID:            clientID,
		RedirectURI:         "http://127.0.0.1:53121/callback",
		State:               "state-value",
		Nonce:               "nonce-value",
		Prompt:              oidc.SpaceDelimitedArray{oidc.PromptLogin},
		MaxAge:              &maxAge,
		UILocales:           oidc.ParseLocales([]string{"fa"}),
		LoginHint:           "alice",
		CodeChallenge:       testChallenge,
		CodeChallengeMethod: oidc.CodeChallengeMethodS256,
	}
}

func finishLogin(r v1alpha1.AuthRequest) v1alpha1.AuthRequest {
	r.Status.Subject = "alice"
	r.Status.AMR = []string{"pwd"}
	r.Status.AuthTime = &metav1.Time{Time: testNow}
	r.Status.Session = "session-name"
	r.Status.Done = true
	return r
}

func TestFromOIDC(t *testing.T) {
	got := FromOIDC(authorizeRequest("bedrock-cli"), "abc", testNow)
	if got.Name != "abc" || got.Namespace != release.SystemNamespace || got.Labels[v1alpha1.LabelKind] != "AuthRequest" || got.Labels[v1alpha1.LabelName] != "abc" {
		t.Fatalf("metadata %+v", got.ObjectMeta)
	}
	if got.Spec.MaxAge == nil || *got.Spec.MaxAge != 300 || !reflect.DeepEqual(got.Spec.UILocales, []string{"fa"}) || !reflect.DeepEqual(got.Spec.Prompt, []string{"login"}) {
		t.Fatalf("converted fields %+v", got.Spec)
	}
	if !got.Spec.ExpiresAt.Time.Equal(testNow.Add(30*time.Minute)) || got.Spec.CodeChallengeMethod != "S256" || got.Spec.ResponseType != "code" {
		t.Fatalf("spec %+v", got.Spec)
	}
}

func TestAuthRequestRoundTrip(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"))
	s := newTestStore(c, testNow, testSettings(), nil)
	created, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), "")
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetID()
	if len(id) != 26 || strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("request id %q must have 26 lowercase letters and digits", id)
	}
	found, err := s.AuthRequestByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if found.GetClientID() != "bedrock-cli" || found.GetRedirectURI() != "http://127.0.0.1:53121/callback" || found.GetState() != "state-value" || found.GetNonce() != "nonce-value" || found.GetResponseType() != oidc.ResponseTypeCode {
		t.Fatalf("request %+v", found)
	}
	if !reflect.DeepEqual(found.GetScopes(), []string{"openid", "offline_access", "groups"}) || !reflect.DeepEqual(found.GetAudience(), []string{"bedrock", "bedrock-cli"}) {
		t.Fatalf("scopes %v audience %v", found.GetScopes(), found.GetAudience())
	}
	if !reflect.DeepEqual(found.GetCodeChallenge(), &oidc.CodeChallenge{Challenge: testChallenge, Method: oidc.CodeChallengeMethodS256}) {
		t.Fatalf("code challenge %+v", found.GetCodeChallenge())
	}
	if found.Done() || found.GetSubject() != "" || !found.GetAuthTime().IsZero() {
		t.Fatal("a new request is not done and has no subject")
	}
	saved, err := s.SaveLogin(ctx, id, finishLogin)
	if err != nil || !saved.Status.Done {
		t.Fatalf("save login %+v, err %v", saved.Status, err)
	}
	done, err := s.AuthRequestByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !done.Done() || done.GetSubject() != "alice" || !reflect.DeepEqual(done.GetAMR(), []string{"pwd"}) || !done.GetAuthTime().Equal(testNow) || sessionOf(done) != "session-name" {
		t.Fatalf("finished request %+v", done)
	}
	if err := s.DeleteAuthRequest(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthRequestByID(ctx, id); !apierrors.IsNotFound(err) {
		t.Fatalf("a deleted request must be not found, got %v", err)
	}
	if err := s.DeleteAuthRequest(ctx, id); err != nil {
		t.Fatalf("deleting twice must not fail: %v", err)
	}
}

func TestPublicClientNeedsPKCE(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"), confidentialClient("console", "console-secret", v1alpha1.GrantAuthorizationCode))
	s := newTestStore(c, testNow, testSettings(), nil)

	missing := authorizeRequest("bedrock-cli")
	missing.CodeChallenge = ""
	missing.CodeChallengeMethod = ""
	plain := authorizeRequest("bedrock-cli")
	plain.CodeChallengeMethod = oidc.CodeChallengeMethodPlain
	for name, req := range map[string]*oidc.AuthRequest{"no challenge": missing, "plain": plain} {
		if _, err := s.CreateAuthRequest(ctx, req, ""); err == nil {
			t.Fatalf("%s: a public client without S256 must be refused", name)
		}
	}

	evil := authorizeRequest("bedrock-cli")
	evil.RedirectURI = "http://localhost:53121/callback"
	_, err := s.CreateAuthRequest(ctx, evil, "")
	var oidcErr *oidc.Error
	if !errors.As(err, &oidcErr) || oidcErr.ErrorType != oidc.InvalidRequest || !oidcErr.IsRedirectDisabled() {
		t.Fatalf("an unregistered redirect must fail without a redirect, got %v", err)
	}

	confidential := authorizeRequest("console")
	confidential.RedirectURI = "https://console.example.test/oauth/callback"
	confidential.CodeChallenge = ""
	confidential.CodeChallengeMethod = ""
	if _, err := s.CreateAuthRequest(ctx, confidential, ""); err != nil {
		t.Fatalf("a confidential client may skip PKCE: %v", err)
	}
	var list v1alpha1.AuthRequestList
	if err := c.List(ctx, &list, client.InNamespace(release.SystemNamespace)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Spec.ClientID != "console" {
		t.Fatalf("only the valid request may be stored, got %d", len(list.Items))
	}
}

func TestAuthCodeWorksOnce(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"))
	s := newTestStore(c, testNow, testSettings(), nil)
	created, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), "")
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetID()
	if err := s.SaveAuthCode(ctx, id, "code-before-login"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthRequestByCode(ctx, "code-before-login"); err == nil {
		t.Fatal("a code of an unfinished login must fail")
	}
	if _, err := s.SaveLogin(ctx, id, finishLogin); err != nil {
		t.Fatal(err)
	}
	code := "opaque-code-value"
	if err := s.SaveAuthCode(ctx, id, code); err != nil {
		t.Fatal(err)
	}
	var stored v1alpha1.AuthCode
	if err := c.Get(ctx, objectKey(secret.SHA256Hex(code)), &stored); err != nil {
		t.Fatalf("the code must be stored under its SHA-256: %v", err)
	}
	if stored.Spec.AuthRequest != id || !stored.Spec.ExpiresAt.Time.Equal(testNow.Add(60*time.Second)) || len(stored.Labels[v1alpha1.LabelName]) != 63 {
		t.Fatalf("stored code %+v", stored)
	}
	if err := c.Get(ctx, objectKey(code), &v1alpha1.AuthCode{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the plain code must never be an object name, got %v", err)
	}
	request, err := s.AuthRequestByCode(ctx, code)
	if err != nil || request.GetID() != id {
		t.Fatalf("request %v, err %v", request, err)
	}
	if _, err := s.AuthRequestByCode(ctx, code); err == nil {
		t.Fatal("a code must work once")
	}
}

func TestAuthCodeDeleteRejectsStaleRead(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"))
	s := newTestStore(c, testNow, testSettings(), nil)
	created, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), "")
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetID()
	if _, err := s.SaveLogin(ctx, id, finishLogin); err != nil {
		t.Fatal(err)
	}
	code := "racing-code-value"
	if err := s.SaveAuthCode(ctx, id, code); err != nil {
		t.Fatal(err)
	}

	hook := &hookClient{Client: c}
	hook.afterGet = func() {
		if _, err := s.AuthRequestByCode(ctx, code); err != nil {
			t.Fatalf("setup: the first consumption via the plain store must succeed: %v", err)
		}
	}
	racing := newTestStore(hook, testNow, testSettings(), nil)

	if _, err := racing.AuthRequestByCode(ctx, code); err == nil {
		t.Fatal("a stale read racing a concurrent consumption must be refused")
	}
	if err := c.Get(ctx, objectKey(secret.SHA256Hex(code)), &v1alpha1.AuthCode{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the code must stay consumed after the rejected stale delete, got %v", err)
	}
}

func TestExpiredRequestIsNotFound(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"))
	s := newTestStore(c, testNow, testSettings(), nil)
	created, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), "")
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetID()
	if _, err := s.SaveLogin(ctx, id, finishLogin); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthCode(ctx, id, "late-code"); err != nil {
		t.Fatal(err)
	}
	late := newTestStore(c, testNow.Add(30*time.Minute), testSettings(), nil)
	if _, err := late.AuthRequestByID(ctx, id); !isNotFound(err) {
		t.Fatalf("an expired request must be not found, got %v", err)
	}
	if _, err := late.SaveLogin(ctx, id, finishLogin); !isNotFound(err) {
		t.Fatalf("an expired request must not be saved, got %v", err)
	}
	lateCode := newTestStore(c, testNow.Add(60*time.Second), testSettings(), nil)
	if _, err := lateCode.AuthRequestByCode(ctx, "late-code"); !isNotFound(err) {
		t.Fatalf("an expired code must be not found, got %v", err)
	}
}

func TestSaveLoginRetriesOnConflict(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, publicClient("bedrock-cli"))
	s := newTestStore(c, testNow, testSettings(), nil)
	created, err := s.CreateAuthRequest(ctx, authorizeRequest("bedrock-cli"), "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	saved, err := s.SaveLogin(ctx, created.GetID(), func(r v1alpha1.AuthRequest) v1alpha1.AuthRequest {
		calls++
		if calls == 1 {
			concurrent := r.DeepCopy()
			concurrent.Status.Login.Error = "concurrent"
			if err := c.Status().Update(ctx, concurrent); err != nil {
				t.Fatal(err)
			}
		}
		r.Status.Login.Completed = append(r.Status.Login.Completed, v1alpha1.MethodPassword)
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("the update must run again after a conflict, ran %d times", calls)
	}
	if saved.Status.Login.Error != "concurrent" || !reflect.DeepEqual(saved.Status.Login.Completed, []string{"password"}) {
		t.Fatalf("saved login %+v", saved.Status.Login)
	}
	var stored v1alpha1.AuthRequest
	if err := c.Get(ctx, objectKey(created.GetID()), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Status.Login, saved.Status.Login) {
		t.Fatalf("stored login %+v, returned %+v", stored.Status.Login, saved.Status.Login)
	}
}
