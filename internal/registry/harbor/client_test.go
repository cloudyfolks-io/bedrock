package harbor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seen struct {
	method  string
	uri     string
	header  http.Header
	body    string
	user    string
	pass    string
	hasAuth bool
}

type route struct {
	status int
	body   string
	header map[string]string
}

func fakeHarbor(t *testing.T, routes map[string]route) (*Client, *[]seen) {
	t.Helper()
	var calls []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		calls = append(calls, seen{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), body: string(raw), user: user, pass: pass, hasAuth: ok})
		answer, found := routes[r.Method+" "+r.RequestURI]
		if !found {
			answer, found = routes[r.Method+" "+strings.SplitN(r.RequestURI, "?", 2)[0]]
		}
		if !found {
			t.Errorf("unexpected call %s %s", r.Method, r.RequestURI)
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		for key, value := range answer.header {
			w.Header().Set(key, value)
		}
		status := answer.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer.body)
	}))
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, Username: "admin", Password: "admin-password-value"}, &calls
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("body %q: %v", raw, err)
	}
	return out
}

func TestCreateProjectSendsPrivateProjectWithQuota(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"POST /api/v2.0/projects": {status: 201}})
	if err := c.CreateProject(context.Background(), "acme", 5); err != nil {
		t.Fatal(err)
	}
	call := (*calls)[0]
	if !call.hasAuth || call.user != "admin" || call.pass != "admin-password-value" {
		t.Errorf("auth %+v", call)
	}
	body := decode(t, call.body)
	if body["project_name"] != "acme" || body["storage_limit"] != float64(5*1024*1024*1024) {
		t.Errorf("body %v", body)
	}
	if body["metadata"].(map[string]any)["public"] != "false" {
		t.Errorf("metadata %v", body["metadata"])
	}
}

func TestCreateProjectWithoutQuotaIsUnlimited(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"POST /api/v2.0/projects": {status: 201}})
	if err := c.CreateProject(context.Background(), "acme", 0); err != nil {
		t.Fatal(err)
	}
	if decode(t, (*calls)[0].body)["storage_limit"] != float64(-1) {
		t.Errorf("body %s", (*calls)[0].body)
	}
}

func TestErrorsAreMappedToTypedErrors(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusConflict, ErrConflict},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusBadGateway, ErrUnavailable},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusInternalServerError, ErrUnavailable},
	}
	for _, test := range tests {
		c, _ := fakeHarbor(t, map[string]route{"GET /api/v2.0/projects/acme": {status: test.status, body: `{"errors":[{"code":"X","message":"boom"}]}`}})
		_, err := c.GetProject(context.Background(), "acme")
		if !errors.Is(err, test.want) {
			t.Errorf("status %d: got %v", test.status, err)
		}
	}
	c, _ := fakeHarbor(t, map[string]route{"GET /api/v2.0/projects/acme": {status: http.StatusForbidden, body: `{"errors":[{"message":"denied"}]}`}})
	_, err := c.GetProject(context.Background(), "acme")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "denied") {
		t.Errorf("forbidden: %v", err)
	}
}

func TestUnreachableHarborIsUnavailable(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", Username: "admin", Password: "admin-password-value"}
	if _, err := c.GetProject(context.Background(), "acme"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v", err)
	}
}

func TestProjectPathsMarkTheNameAsAName(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/123":                                   {body: `{"project_id":7,"name":"123","metadata":{"retention_id":"4"}}`},
		"DELETE /api/v2.0/projects/123":                                {status: 200},
		"GET /api/v2.0/projects/123/summary":                           {body: `{"repo_count":2,"quota":{"hard":{"storage":100},"used":{"storage":40}}}`},
		"GET /api/v2.0/projects/123/repositories?page=1&page_size=100": {body: `[{"name":"123/app"}]`, header: map[string]string{"X-Total-Count": "1"}},
	})
	ctx := context.Background()
	project, err := c.GetProject(ctx, "123")
	if err != nil || project.ID != 7 || project.RetentionID != 4 {
		t.Fatalf("project %+v %v", project, err)
	}
	if err := c.DeleteProject(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	summary, err := c.ProjectSummary(ctx, "123")
	if err != nil || summary.UsedBytes != 40 || summary.HardBytes != 100 || summary.RepositoryCount != 2 {
		t.Fatalf("summary %+v %v", summary, err)
	}
	repos, total, err := c.ListRepositories(ctx, "123", 1, 100)
	if err != nil || total != 1 || len(repos) != 1 || repos[0].Name != "123/app" {
		t.Fatalf("repos %v %d %v", repos, total, err)
	}
	for _, call := range *calls {
		if call.header.Get("X-Is-Resource-Name") != "true" {
			t.Errorf("%s %s has no X-Is-Resource-Name", call.method, call.uri)
		}
	}
}

func TestRepositoryTotalIsAtLeastThePageLength(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{"GET /api/v2.0/projects/acme/repositories?page=1&page_size=100": {body: `[{"name":"acme/a"},{"name":"acme/b"}]`}})
	_, total, err := c.ListRepositories(context.Background(), "acme", 1, 100)
	if err != nil || total != 2 {
		t.Fatalf("total %d %v", total, err)
	}
}

func TestDeleteProjectIgnoresAMissingProject(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{"DELETE /api/v2.0/projects/acme": {status: 404}})
	if err := c.DeleteProject(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryNamesAreDoubleEncoded(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"DELETE /api/v2.0/projects/acme/repositories/team%252Fapp": {status: 200}})
	if err := c.DeleteRepository(context.Background(), "acme", "team/app"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].uri != "/api/v2.0/projects/acme/repositories/team%252Fapp" {
		t.Errorf("uri %s", (*calls)[0].uri)
	}
	if (*calls)[0].header.Get("X-Is-Resource-Name") != "true" {
		t.Error("project name is not marked")
	}
}

func TestSetQuotaFindsTheQuotaRowOfTheProject(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme":                           {body: `{"project_id":9,"name":"acme"}`},
		"GET /api/v2.0/quotas?reference=project&reference_id=9": {body: `[{"id":33}]`},
		"PUT /api/v2.0/quotas/33":                               {status: 200},
	})
	if err := c.SetQuota(context.Background(), "acme", 2); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || (*calls)[2].method != http.MethodPut {
		t.Fatalf("calls %+v", *calls)
	}
	hard := decode(t, (*calls)[2].body)["hard"].(map[string]any)
	if hard["storage"] != float64(2*1024*1024*1024) {
		t.Errorf("body %s", (*calls)[2].body)
	}
}

func TestSetQuotaWithoutARowFails(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme":                           {body: `{"project_id":9}`},
		"GET /api/v2.0/quotas?reference=project&reference_id=9": {body: `[]`},
	})
	if err := c.SetQuota(context.Background(), "acme", 2); err == nil {
		t.Fatal("no error")
	}
}

func TestCreateRobotSendsAProjectRobotWithoutExpiry(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"POST /api/v2.0/robots": {status: 201, body: `{"id":12,"name":"key_acme+ci","secret":"robot-secret-value"}`}})
	robot, secret, err := c.CreateRobot(context.Background(), "acme", "ci", PermissionPushPull)
	if err != nil {
		t.Fatal(err)
	}
	if robot.ID != 12 || robot.Name != "key_acme+ci" || secret != "robot-secret-value" {
		t.Errorf("robot %+v secret %q", robot, secret)
	}
	body := decode(t, (*calls)[0].body)
	if body["name"] != "ci" || body["level"] != "project" || body["duration"] != float64(-1) {
		t.Errorf("body %v", body)
	}
	grant := body["permissions"].([]any)[0].(map[string]any)
	if grant["kind"] != "project" || grant["namespace"] != "acme" || len(grant["access"].([]any)) != 2 {
		t.Errorf("permissions %v", grant)
	}
}

func TestCreateRobotWithAnUnknownPermissionFailsBeforeTheCall(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{})
	if _, _, err := c.CreateRobot(context.Background(), "acme", "ci", "root"); err == nil {
		t.Fatal("no error")
	}
	if len(*calls) != 0 {
		t.Errorf("calls %v", *calls)
	}
}

func TestCreateRobotConflict(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{"POST /api/v2.0/robots": {status: 409, body: `{"errors":[{"message":"exists"}]}`}})
	if _, _, err := c.CreateRobot(context.Background(), "acme", "ci", PermissionPull); !errors.Is(err, ErrConflict) {
		t.Errorf("got %v", err)
	}
}

func TestSetRobotSecretPatchesTheRobot(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"PATCH /api/v2.0/robots/12": {status: 200}})
	if err := c.SetRobotSecret(context.Background(), 12, "new-robot-secret-value"); err != nil {
		t.Fatal(err)
	}
	if decode(t, (*calls)[0].body)["secret"] != "new-robot-secret-value" {
		t.Errorf("body %s", (*calls)[0].body)
	}
}

func TestFindRobotMatchesTheNameInTheProject(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme": {body: `{"project_id":9}`},
		"GET /api/v2.0/robots?page=1&page_size=100&q=Level%3Dproject%2CProjectID%3D9": {body: `[{"id":1,"name":"key_acme+other","permissions":[{"kind":"project","namespace":"acme"}]},{"id":2,"name":"key_acme+ci","permissions":[{"kind":"project","namespace":"acme"}]}]`},
	})
	robot, found, err := c.FindRobot(context.Background(), "acme", "ci")
	if err != nil || !found || robot.ID != 2 {
		t.Fatalf("robot %+v %v %v", robot, found, err)
	}
	_, found, err = c.FindRobot(context.Background(), "acme", "missing")
	if err != nil || found {
		t.Fatalf("found %v %v", found, err)
	}
}

func TestListRobotsReturnsEveryRobotOfTheProject(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme": {body: `{"project_id":9}`},
		"GET /api/v2.0/robots?page=1&page_size=100&q=Level%3Dproject%2CProjectID%3D9": {body: `[{"id":1,"name":"key_acme+a","permissions":[{"kind":"project","namespace":"acme"}]},{"id":2,"name":"key_acme+b","permissions":[{"kind":"project","namespace":"acme"}]}]`},
	})
	robots, err := c.ListRobots(context.Background(), "acme")
	if err != nil || len(robots) != 2 || robots[0].ID != 1 || robots[1].Name != "key_acme+b" {
		t.Fatalf("robots %v %v", robots, err)
	}
}

func TestDeleteWebhookRemovesThePolicyByName(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme/webhook/policies?page=1&page_size=100": {body: `[{"id":2,"name":"other"},{"id":3,"name":"platform-events"}]`},
		"DELETE /api/v2.0/projects/acme/webhook/policies/3":                 {status: 200},
	})
	if err := c.DeleteWebhook(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[1].method != http.MethodDelete {
		t.Errorf("calls %+v", *calls)
	}
	c, calls = fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme/webhook/policies?page=1&page_size=100": {body: `[{"id":2,"name":"other"}]`},
	})
	if err := c.DeleteWebhook(context.Background(), "acme"); err != nil || len(*calls) != 1 {
		t.Errorf("a missing policy: %v %+v", err, *calls)
	}
}

func TestDeleteRobotIgnoresAMissingRobot(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{"DELETE /api/v2.0/robots/12": {status: 404}})
	if err := c.DeleteRobot(context.Background(), 12); err != nil {
		t.Fatal(err)
	}
}

func TestSetRetentionCreatesThenUpdates(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme": {body: `{"project_id":9,"metadata":{}}`},
		"POST /api/v2.0/retentions":   {status: 201},
	})
	retention, err := BuildRetention(RetentionInput{KeepLastN: ptr(5), Schedule: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetRetention(context.Background(), "acme", retention); err != nil {
		t.Fatal(err)
	}
	body := decode(t, (*calls)[1].body)
	scope := body["scope"].(map[string]any)
	if scope["level"] != "project" || scope["ref"] != float64(9) {
		t.Errorf("scope %v", scope)
	}

	c, calls = fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme": {body: `{"project_id":9,"metadata":{"retention_id":"4"}}`},
		"PUT /api/v2.0/retentions/4":  {status: 200},
	})
	if err := c.SetRetention(context.Background(), "acme", retention); err != nil {
		t.Fatal(err)
	}
	if decode(t, (*calls)[1].body)["id"] != float64(4) {
		t.Errorf("body %s", (*calls)[1].body)
	}
}

func TestGetRetentionIsNilWithoutAPolicy(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{"GET /api/v2.0/projects/acme": {body: `{"project_id":9,"metadata":{}}`}})
	policy, err := c.GetRetention(context.Background(), "acme")
	if err != nil || policy != nil {
		t.Fatalf("policy %v %v", policy, err)
	}
}

func TestGetRetentionReadsThePolicy(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme": {body: `{"project_id":9,"metadata":{"retention_id":"4"}}`},
		"GET /api/v2.0/retentions/4":  {body: `{"id":4,"algorithm":"or","rules":[{"template":"latestPushedK","params":{"latestPushedK":5}}]}`},
	})
	policy, err := c.GetRetention(context.Background(), "acme")
	if err != nil || policy == nil || policy.ID != 4 || len(policy.Rules) != 1 {
		t.Fatalf("policy %v %v", policy, err)
	}
}

func TestEnsureWebhookCreatesThenUpdatesByName(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme/webhook/policies?page=1&page_size=100": {body: `[]`},
		"POST /api/v2.0/projects/acme/webhook/policies":                     {status: 201},
	})
	if err := c.EnsureWebhook(context.Background(), "acme", "https://hooks.example.test/x", "Bearer token-value"); err != nil {
		t.Fatal(err)
	}
	body := decode(t, (*calls)[1].body)
	target := body["targets"].([]any)[0].(map[string]any)
	if body["name"] != "platform-events" || target["address"] != "https://hooks.example.test/x" || target["auth_header"] != "Bearer token-value" {
		t.Errorf("body %v", body)
	}
	events := body["event_types"].([]any)
	if len(events) != 2 || events[0] != "PUSH_ARTIFACT" || events[1] != "DELETE_ARTIFACT" {
		t.Errorf("events %v", events)
	}

	c, calls = fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme/webhook/policies?page=1&page_size=100": {body: `[{"id":3,"name":"platform-events"}]`},
		"PUT /api/v2.0/projects/acme/webhook/policies/3":                    {status: 200},
	})
	if err := c.EnsureWebhook(context.Background(), "acme", "https://hooks.example.test/x", ""); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].method != http.MethodPut {
		t.Errorf("calls %+v", *calls)
	}
}

func TestSetConfigurationsPutsTheBody(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"PUT /api/v2.0/configurations": {status: 200}})
	if err := c.SetConfigurations(context.Background(), map[string]any{"auth_mode": "db_auth", "self_registration": false}); err != nil {
		t.Fatal(err)
	}
	body := decode(t, (*calls)[0].body)
	if body["auth_mode"] != "db_auth" || body["self_registration"] != false {
		t.Errorf("body %v", body)
	}
}

func TestScheduleGCCreatesThenUpdatesOnConflict(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"POST /api/v2.0/system/gc/schedule": {status: 409},
		"PUT /api/v2.0/system/gc/schedule":  {status: 200},
	})
	if err := c.ScheduleGC(context.Background(), "0 0 3 * * *"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls %+v", *calls)
	}
	schedule := decode(t, (*calls)[1].body)["schedule"].(map[string]any)
	if schedule["type"] != "Custom" || schedule["cron"] != "0 0 3 * * *" {
		t.Errorf("schedule %v", schedule)
	}
}

func TestEnsureRegistryEndpointCreates(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{
		"POST /api/v2.0/registries": {status: 201, header: map[string]string{"Location": "/api/v2.0/registries/8"}},
	})
	id, err := c.EnsureRegistryEndpoint(context.Background(), "mirror-docker", "docker-hub", "https://hub.docker.com")
	if err != nil || id != 8 {
		t.Fatalf("id %d %v", id, err)
	}
	body := decode(t, (*calls)[0].body)
	if body["name"] != "mirror-docker" || body["type"] != "docker-hub" || body["url"] != "https://hub.docker.com" || body["insecure"] != false {
		t.Errorf("body %v", body)
	}
}

func TestEnsureRegistryEndpointAcceptsAConflictWithTheSameType(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{
		"POST /api/v2.0/registries": {status: 409},
		"GET /api/v2.0/registries?page=1&page_size=100&q=name%3Dmirror-docker": {body: `[{"id":8,"name":"mirror-docker","type":"docker-hub","url":"https://old.example.test"}]`},
	})
	id, err := c.EnsureRegistryEndpoint(context.Background(), "mirror-docker", "docker-hub", "https://hub.docker.com")
	if err != nil || id != 8 {
		t.Fatalf("id %d %v", id, err)
	}
}

func TestEnsureRegistryEndpointRejectsAConflictWithAnotherType(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{
		"POST /api/v2.0/registries": {status: 409},
		"GET /api/v2.0/registries?page=1&page_size=100&q=name%3Dmirror-docker": {body: `[{"id":8,"name":"mirror-docker","type":"github-ghcr","url":"https://ghcr.io"}]`},
	})
	_, err := c.EnsureRegistryEndpoint(context.Background(), "mirror-docker", "docker-hub", "https://hub.docker.com")
	if !errors.Is(err, ErrTypeMismatch) || !strings.Contains(err.Error(), "github-ghcr") || !strings.Contains(err.Error(), "docker-hub") {
		t.Errorf("got %v", err)
	}
}

func TestEnsureProxyProjectBindsTheEndpointAndIgnoresAConflict(t *testing.T) {
	c, calls := fakeHarbor(t, map[string]route{"POST /api/v2.0/projects": {status: 409}})
	if err := c.EnsureProxyProject(context.Background(), "mirror-docker", 8); err != nil {
		t.Fatal(err)
	}
	body := decode(t, (*calls)[0].body)
	if body["project_name"] != "mirror-docker" || body["registry_id"] != float64(8) || body["metadata"].(map[string]any)["public"] != "true" {
		t.Errorf("body %v", body)
	}
}

func TestHealthReportsUnhealthyComponents(t *testing.T) {
	c, _ := fakeHarbor(t, map[string]route{"GET /api/v2.0/health": {body: `{"status":"healthy"}`}})
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ = fakeHarbor(t, map[string]route{"GET /api/v2.0/health": {body: `{"status":"unhealthy","components":[{"name":"database","status":"unhealthy"}]}`}})
	if err := c.Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v", err)
	}
}

func TestNoErrorRevealsASecret(t *testing.T) {
	secret := "robot-secret-value"
	c, _ := fakeHarbor(t, map[string]route{
		"PATCH /api/v2.0/robots/12": {status: 400, body: `{"errors":[{"message":"bad secret robot-secret-value and admin-password-value"}]}`},
		"POST /api/v2.0/robots":     {status: 500, body: `{"errors":[{"message":"admin-password-value"}]}`},
	})
	err := c.SetRobotSecret(context.Background(), 12, secret)
	if err == nil {
		t.Fatal("no error")
	}
	for _, text := range []string{secret, "admin-password-value", "Authorization", "Basic"} {
		if strings.Contains(err.Error(), text) {
			t.Errorf("error %q holds %q", err, text)
		}
	}
	_, _, err = c.CreateRobot(context.Background(), "acme", "ci", PermissionPull)
	if err == nil || strings.Contains(err.Error(), "admin-password-value") {
		t.Errorf("error %v", err)
	}
}

func TestAnswersAreLimitedToOneMebibyte(t *testing.T) {
	big := `[{"name":"` + strings.Repeat("a", 2<<20) + `"}]`
	c, _ := fakeHarbor(t, map[string]route{"GET /api/v2.0/projects/acme/repositories?page=1&page_size=100": {body: big}})
	if _, _, err := c.ListRepositories(context.Background(), "acme", 1, 100); err == nil {
		t.Fatal("no error for an answer over 1 MiB")
	}
}

func ptr(value int) *int { return &value }

func robotPage(from, to int, project string) string {
	items := []string{}
	for id := from; id <= to; id++ {
		items = append(items, fmt.Sprintf(`{"id":%d,"name":"key_%s+r%d","creation_time":"2026-10-01T10:00:00Z","permissions":[{"kind":"project","namespace":"%s"}]}`, id, project, id, project))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestListRobotsReadsEveryPage(t *testing.T) {
	query := "&page_size=100&q=Level%3Dproject%2CProjectID%3D9"
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme":         {body: `{"project_id":9}`},
		"GET /api/v2.0/robots?page=1" + query: {body: robotPage(1, 100, "acme")},
		"GET /api/v2.0/robots?page=2" + query: {body: robotPage(101, 200, "acme")},
		"GET /api/v2.0/robots?page=3" + query: {body: robotPage(201, 250, "acme")},
	})
	robots, err := c.ListRobots(context.Background(), "acme")
	if err != nil || len(robots) != 250 {
		t.Fatalf("robots %d %v", len(robots), err)
	}
	found, ok, err := c.FindRobot(context.Background(), "acme", "r250")
	if err != nil || !ok || found.ID != 250 {
		t.Fatalf("find %+v %v %v", found, ok, err)
	}
}

func TestListRobotsStopsAfterAnExactFullPageThenAnEmptyOne(t *testing.T) {
	query := "&page_size=100&q=Level%3Dproject%2CProjectID%3D9"
	c, calls := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme":         {body: `{"project_id":9}`},
		"GET /api/v2.0/robots?page=1" + query: {body: robotPage(1, 100, "acme")},
		"GET /api/v2.0/robots?page=2" + query: {body: `[]`},
	})
	robots, err := c.ListRobots(context.Background(), "acme")
	if err != nil || len(robots) != 100 || len(*calls) != 3 {
		t.Fatalf("robots %d calls %d %v", len(robots), len(*calls), err)
	}
}

func TestListRobotsIgnoresRobotsOfOtherProjectsWhateverTheFilterReturns(t *testing.T) {
	query := "&page_size=100&q=Level%3Dproject%2CProjectID%3D9"
	foreign := `{"id":900,"name":"key_other+ci","permissions":[{"kind":"project","namespace":"other"}]}`
	noPermissions := `{"id":901,"name":"key_acme+ci"}`
	system := `{"id":902,"name":"key_acme+ci","permissions":[{"kind":"system","namespace":"/"}]}`
	own := `{"id":2,"name":"key_acme+ci","permissions":[{"kind":"project","namespace":"acme"}]}`
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme":         {body: `{"project_id":9}`},
		"GET /api/v2.0/robots?page=1" + query: {body: "[" + foreign + "," + noPermissions + "," + system + "," + own + "]"},
	})
	robots, err := c.ListRobots(context.Background(), "acme")
	if err != nil || len(robots) != 1 || robots[0].ID != 2 {
		t.Fatalf("robots %+v %v", robots, err)
	}
	robot, found, err := c.FindRobot(context.Background(), "acme", "ci")
	if err != nil || !found || robot.ID != 2 {
		t.Fatalf("find %+v %v %v", robot, found, err)
	}
}

func TestRobotsAndProjectsCarryTheirCreationTime(t *testing.T) {
	query := "&page_size=100&q=Level%3Dproject%2CProjectID%3D9"
	c, _ := fakeHarbor(t, map[string]route{
		"GET /api/v2.0/projects/acme":         {body: `{"project_id":9,"creation_time":"2026-10-02T11:00:00Z"}`},
		"GET /api/v2.0/robots?page=1" + query: {body: robotPage(1, 1, "acme")},
	})
	project, err := c.GetProject(context.Background(), "acme")
	if err != nil || project.Created.Format("2006-01-02T15:04:05Z") != "2026-10-02T11:00:00Z" {
		t.Fatalf("project %+v %v", project, err)
	}
	robots, _ := c.ListRobots(context.Background(), "acme")
	if len(robots) != 1 || robots[0].Created.Format("2006-01-02T15:04:05Z") != "2026-10-01T10:00:00Z" {
		t.Fatalf("robots %+v", robots)
	}
}
