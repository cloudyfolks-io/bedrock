package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

const buildLogsPath = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web/builds/shop/logs"

func buildRouter(t *testing.T, allow func(authorizationv1.ResourceAttributes) bool) (*reviewAPI, *calledHandler, http.Handler) {
	t.Helper()
	subs := NewSubresources()
	api := newReviewAPI(t, allow)
	called := &calledHandler{}
	if err := subs.Register("builds", "logs", called.handler(http.StatusOK)); err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	return api, called, SubresourceRouter(subs, api.upstream(t), fakeReader(t), next)
}

func TestBuildLogsAreAuthorizedWithTheGetVerb(t *testing.T) {
	api, called, router := buildRouter(t, func(a authorizationv1.ResourceAttributes) bool {
		return a.Resource == "builds" && a.Subresource == "logs" && a.Verb == "get"
	})
	rec := do(asCaller(router, "viewer-token"), httptest.NewRequest(http.MethodGet, buildLogsPath, nil))
	if rec.Code != http.StatusOK || len(called.calls()) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	want := authorizationv1.ResourceAttributes{Namespace: "acme-web", Verb: "get", Group: "bedrock.cloudyfolks.io", Resource: "builds", Subresource: "logs", Name: "shop"}
	if got := api.reviews(); len(got) != 1 || got[0] != want {
		t.Fatalf("reviews %+v", got)
	}
}

func TestBuildLogsRefuseAUserWithoutTheRole(t *testing.T) {
	api, called, router := buildRouter(t, func(authorizationv1.ResourceAttributes) bool { return false })
	rec := do(asCaller(router, "stranger-token"), httptest.NewRequest(http.MethodGet, buildLogsPath, nil))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"forbidden"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(called.calls()) != 0 || len(api.reviews()) != 1 {
		t.Fatalf("a refused request reached the handler: %v", called.calls())
	}
}

func TestBuildLogsNeedACaller(t *testing.T) {
	_, called, router := buildRouter(t, allowAll)
	rec := do(router, httptest.NewRequest(http.MethodGet, buildLogsPath, nil))
	if rec.Code != http.StatusUnauthorized || len(called.calls()) != 0 {
		t.Fatalf("%d", rec.Code)
	}
}
