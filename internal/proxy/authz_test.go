package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type authzAnswer struct {
	Results []accessResult `json:"results"`
}

func authzPost(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, PathAuthz, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return do(handler, req)
}

func checksBody(checks ...string) string {
	return `{"checks":[` + strings.Join(checks, ",") + `]}`
}

func createIfMissing(t *testing.T, objects ...client.Object) {
	t.Helper()
	for _, obj := range objects {
		if err := testClient.Create(context.Background(), obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
}

func seedAliceRBAC(t *testing.T) {
	t.Helper()
	startEnv(t)
	subject := rbacv1.Subject{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "bedrock:alice"}
	createIfMissing(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme-web"}},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "ops-reader"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"settings"}, Verbs: []string{"get", "list"}}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "ops-reader"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "ops-reader"},
			Subjects:   []rbacv1.Subject{subject},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "vm-lister", Namespace: "acme-web"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"bedrock.cloudyfolks.io"}, Resources: []string{"virtualmachines"}, Verbs: []string{"list"}}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "vm-lister", Namespace: "acme-web"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "vm-lister"},
			Subjects:   []rbacv1.Subject{subject},
		},
	)
}

func TestAuthzBatchKeepsOrder(t *testing.T) {
	seedAliceRBAC(t)
	u, err := NewUpstream(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	handler := asCaller(AuthzHandler(u), "token-alice")
	body := checksBody(
		`{"verb":"get","group":"bedrock.cloudyfolks.io","resource":"settings"}`,
		`{"verb":"delete","group":"bedrock.cloudyfolks.io","resource":"hosts"}`,
		`{"verb":"list","group":"bedrock.cloudyfolks.io","resource":"virtualmachines","namespace":"acme-web"}`,
	)
	var got authzAnswer
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		rec := authzPost(handler, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		got = authzAnswer{}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Results) == 3 && got.Results[0].Allowed && got.Results[2].Allowed {
			break
		}
	}
	if len(got.Results) != 3 || !got.Results[0].Allowed || got.Results[1].Allowed || !got.Results[2].Allowed {
		t.Fatalf("results %+v", got.Results)
	}
}

func TestAuthzBatchLimits(t *testing.T) {
	api := newReviewAPI(t, allowAll)
	handler := asCaller(AuthzHandler(api.upstream(t)), "caller-token")
	many := make([]string, 101)
	for i := range many {
		many[i] = `{"verb":"get","resource":"settings"}`
	}
	bodies := map[string]string{
		"101 checks":      checksBody(many...),
		"unknown verb":    checksBody(`{"verb":"explode","resource":"settings"}`),
		"empty resource":  checksBody(`{"verb":"get","resource":""}`),
		"missing verb":    checksBody(`{"resource":"settings"}`),
		"long string":     checksBody(`{"verb":"get","resource":"` + strings.Repeat("a", 254) + `"}`),
		"not JSON":        `not json`,
		"wrong structure": `{"checks":"x"}`,
	}
	for name, body := range bodies {
		if rec := authzPost(handler, body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(api.reviews()) != 0 {
		t.Fatalf("API saw %d reviews for invalid bodies", len(api.reviews()))
	}
	exact := checksBody(many[:100]...)
	if rec := authzPost(handler, exact); rec.Code != http.StatusOK {
		t.Fatalf("100 checks: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthzBatchRunsAtMostEightInParallel(t *testing.T) {
	var running, peak atomic.Int32
	api := newFakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		now := running.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":true}}`))
	})
	many := make([]string, 50)
	for i := range many {
		many[i] = `{"verb":"get","resource":"settings","name":"n` + strconv.Itoa(i) + `"}`
	}
	rec := authzPost(asCaller(AuthzHandler(api.upstream(t)), "caller-token"), checksBody(many...))
	if rec.Code != http.StatusOK || peak.Load() != reviewParallel {
		t.Fatalf("%d, peak concurrency %d", rec.Code, peak.Load())
	}
}

func TestAuthzBatchUpstreamFailure(t *testing.T) {
	api := newReviewAPI(t, allowAll)
	u := api.upstream(t)
	api.server.Close()
	rec := authzPost(asCaller(AuthzHandler(u), "caller-token"), checksBody(`{"verb":"get","resource":"settings"}`))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"upstream_unavailable"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthzBatchNeedsCSRFOnTheConsole(t *testing.T) {
	api := newReviewAPI(t, allowAll)
	chain := Authenticate(CookieAuth(aliceSessions()), RequireCSRF(AuthzHandler(api.upstream(t))))
	req := httptest.NewRequest(http.MethodPost, PathAuthz, strings.NewReader(checksBody(`{"verb":"get","resource":"settings"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: "good"})
	if rec := do(chain, req); rec.Code != http.StatusForbidden {
		t.Fatalf("without token: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, PathAuthz, strings.NewReader(checksBody(`{"verb":"get","resource":"settings"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderCSRF, "csrf-alice")
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: "good"})
	if rec := do(chain, req); rec.Code != http.StatusOK {
		t.Fatalf("with token: %d", rec.Code)
	}
}

func projectNamed(name, namespace string) *v1alpha1.Project {
	return &v1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ProjectSpec{TenantRef: "acme", DisplayName: strings.ToUpper(name[:1]) + name[1:]},
		Status:     v1alpha1.ProjectStatus{Namespace: namespace},
	}
}

type projectsAnswer struct {
	Projects []projectView `json:"projects"`
}

func getProjects(t *testing.T, handler http.Handler) projectsAnswer {
	t.Helper()
	rec := do(handler, httptest.NewRequest(http.MethodGet, PathProjects, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got projectsAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func vmListIn(namespaces ...string) func(authorizationv1.ResourceAttributes) bool {
	return func(a authorizationv1.ResourceAttributes) bool {
		return a.Resource == "virtualmachines" && a.Verb == "list" && isOneOf(a.Namespace, namespaces...)
	}
}

func probeCount(api *reviewAPI) int {
	count := 0
	for _, attributes := range api.reviews() {
		if attributes.Resource == "virtualmachines" {
			count++
		}
	}
	return count
}

func TestProjectsEndpointReturnsOnlyProbedProjects(t *testing.T) {
	api := newReviewAPI(t, vmListIn("acme-web"))
	reader := fakeReader(t, projectNamed("web", "acme-web"), projectNamed("db", "acme-db"), projectNamed("other", "zeta-x"))
	got := getProjects(t, asCaller(ProjectsHandler(api.upstream(t), reader), "caller-token"))
	want := []projectView{{Name: "web", DisplayName: "Web", Tenant: "acme", Namespace: "acme-web"}}
	if len(got.Projects) != 1 || got.Projects[0] != want[0] {
		t.Fatalf("projects %+v", got.Projects)
	}
}

func TestProjectsEndpointShortcutForClusterWideList(t *testing.T) {
	api := newReviewAPI(t, func(a authorizationv1.ResourceAttributes) bool { return a.Resource == "projects" && a.Verb == "list" })
	reader := fakeReader(t, projectNamed("web", "acme-web"), projectNamed("db", "acme-db"))
	got := getProjects(t, asCaller(ProjectsHandler(api.upstream(t), reader), "caller-token"))
	if len(got.Projects) != 2 || probeCount(api) != 0 {
		t.Fatalf("projects %+v, probes %d", got.Projects, probeCount(api))
	}
}

func TestProjectsEndpointListsEveryProjectAndCachesReviews(t *testing.T) {
	api := newReviewAPI(t, func(a authorizationv1.ResourceAttributes) bool { return a.Resource != "projects" })
	projects := make([]client.Object, 600)
	for i := range projects {
		projects[i] = projectNamed("p"+strconv.Itoa(1000+i), "ns-"+strconv.Itoa(i))
	}
	handler := asCaller(ProjectsHandler(api.upstream(t), fakeReader(t, projects...)), "caller-token")
	got := getProjects(t, handler)
	if len(got.Projects) != 600 || probeCount(api) != 600 {
		t.Fatalf("probes %d, projects %d", probeCount(api), len(got.Projects))
	}
	reviews := len(api.reviews())
	again := getProjects(t, handler)
	if len(again.Projects) != 600 || len(api.reviews()) != reviews {
		t.Fatalf("second call: %d projects, %d new reviews", len(again.Projects), len(api.reviews())-reviews)
	}
	other := asCaller(ProjectsHandler(api.upstream(t), fakeReader(t, projects...)), "another-token")
	getProjects(t, other)
	if len(api.reviews()) == reviews {
		t.Fatal("a second token must not reuse cached reviews of the first")
	}
}

func TestVisibleProjectsDoesNotReorderItsInput(t *testing.T) {
	api := newReviewAPI(t, func(a authorizationv1.ResourceAttributes) bool { return a.Resource != "projects" })
	input := []v1alpha1.Project{*projectNamed("zulu", "ns-z"), *projectNamed("alpha", "ns-a")}
	reviews, err := kubernetesFor(t, api, "caller-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := visibleProjects(context.Background(), newReviewCache(time.Now).forToken(reviews, "caller-token"), input); err != nil {
		t.Fatal(err)
	}
	if input[0].Name != "zulu" || input[1].Name != "alpha" {
		t.Fatalf("input reordered: %s, %s", input[0].Name, input[1].Name)
	}
}

func kubernetesFor(t *testing.T, api *reviewAPI, token string) (kubernetes.Interface, error) {
	t.Helper()
	return kubernetes.NewForConfig(api.upstream(t).RESTConfig(token))
}

func TestProjectsAreSortedByName(t *testing.T) {
	api := newReviewAPI(t, func(a authorizationv1.ResourceAttributes) bool { return a.Resource != "projects" })
	reader := fakeReader(t, projectNamed("zulu", "ns-z"), projectNamed("alpha", "ns-a"), projectNamed("mike", "ns-m"))
	got := getProjects(t, asCaller(ProjectsHandler(api.upstream(t), reader), "caller-token"))
	names := make([]string, 0, 3)
	for _, project := range got.Projects {
		names = append(names, project.Name)
	}
	if strings.Join(names, ",") != "alpha,mike,zulu" {
		t.Fatalf("names %v", names)
	}
}
