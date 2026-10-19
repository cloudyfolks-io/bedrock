package registry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeProject struct {
	id          int64
	repos       []string
	quota       int64
	retentionID int64
	robots      []fakeRobot
	webhooks    []fakeWebhook
}

type fakeRobot struct {
	id     int64
	name   string
	secret string
	access []any
}

type fakeWebhook struct {
	id   int64
	body map[string]any
}

type harborState struct {
	mu         sync.Mutex
	nextID     int64
	projects   map[string]*fakeProject
	retentions map[int64]map[string]any
	creates    []string
	deletes    []string
	robotSets  []string
	server     *httptest.Server
	secretSeq  int
}

func newHarborState(t *testing.T) *harborState {
	t.Helper()
	state := &harborState{nextID: 100, projects: map[string]*fakeProject{}, retentions: map[int64]map[string]any{}}
	state.server = httptest.NewServer(http.HandlerFunc(state.serve))
	t.Cleanup(state.server.Close)
	return state
}

func (s *harborState) newID() int64 {
	s.nextID++
	return s.nextID
}

func (s *harborState) addProject(name string, quotaGB int64) *fakeProject {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := &fakeProject{id: s.newID(), quota: quotaGB * 1024 * 1024 * 1024}
	s.projects[name] = project
	return project
}

func (s *harborState) addRepos(name string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := s.projects[name]
	for i := range count {
		project.repos = append(project.repos, name+"/app-"+strconv.Itoa(i))
	}
}

func (s *harborState) project(name string) *fakeProject {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects[name]
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (s *harborState) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	path := strings.TrimPrefix(r.URL.Path, "/api/v2.0")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/health":
		reply(w, 200, map[string]any{"status": "healthy"})
	case parts[0] == "projects":
		s.projectRoutes(w, r, parts, body)
	case parts[0] == "quotas":
		s.quotaRoutes(w, r, parts, body)
	case parts[0] == "retentions":
		s.retentionRoutes(w, r, parts, body)
	case parts[0] == "robots":
		s.robotRoutes(w, r, parts, body)
	default:
		reply(w, 404, nil)
	}
}

func (s *harborState) projectRoutes(w http.ResponseWriter, r *http.Request, parts []string, body map[string]any) {
	if len(parts) == 1 && r.Method == http.MethodPost {
		name, _ := body["project_name"].(string)
		if s.projects[name] != nil {
			reply(w, 409, map[string]any{"errors": []map[string]any{{"message": "exists"}}})
			return
		}
		limit, _ := body["storage_limit"].(float64)
		s.projects[name] = &fakeProject{id: s.newID(), quota: int64(limit)}
		s.creates = append(s.creates, name)
		reply(w, 201, nil)
		return
	}
	project := s.projects[parts[1]]
	if project == nil {
		reply(w, 404, map[string]any{"errors": []map[string]any{{"message": "no such project"}}})
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		metadata := map[string]any{}
		if project.retentionID != 0 {
			metadata["retention_id"] = strconv.FormatInt(project.retentionID, 10)
		}
		reply(w, 200, map[string]any{"project_id": project.id, "name": parts[1], "metadata": metadata})
	case len(parts) == 2 && r.Method == http.MethodDelete:
		if len(project.repos) > 0 {
			reply(w, 412, map[string]any{"errors": []map[string]any{{"message": "project has repositories"}}})
			return
		}
		delete(s.projects, parts[1])
		s.deletes = append(s.deletes, parts[1])
		reply(w, 200, nil)
	case parts[2] == "summary":
		reply(w, 200, map[string]any{"repo_count": len(project.repos), "quota": map[string]any{"hard": map[string]any{"storage": project.quota}, "used": map[string]any{"storage": 42}}})
	case parts[2] == "repositories":
		s.repositoryRoutes(w, r, parts, project)
	case parts[2] == "webhook":
		s.webhookRoutes(w, r, parts, project, body)
	default:
		reply(w, 404, nil)
	}
}

func (s *harborState) repositoryRoutes(w http.ResponseWriter, r *http.Request, parts []string, project *fakeProject) {
	if r.Method == http.MethodGet {
		page := project.repos
		if len(page) > 100 {
			page = page[:100]
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(len(project.repos)))
		out := make([]map[string]any, 0, len(page))
		for _, name := range page {
			out = append(out, map[string]any{"name": name})
		}
		reply(w, 200, out)
		return
	}
	once, _ := url.PathUnescape(parts[3])
	name, _ := url.PathUnescape(once)
	full := parts[1] + "/" + name
	for i, existing := range project.repos {
		if existing == full {
			project.repos = append(project.repos[:i], project.repos[i+1:]...)
			reply(w, 200, nil)
			return
		}
	}
	reply(w, 404, nil)
}

func (s *harborState) webhookRoutes(w http.ResponseWriter, r *http.Request, parts []string, project *fakeProject, body map[string]any) {
	switch {
	case r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, hook := range project.webhooks {
			out = append(out, map[string]any{"id": hook.id, "name": hook.body["name"]})
		}
		reply(w, 200, out)
	case r.Method == http.MethodPost:
		project.webhooks = append(project.webhooks, fakeWebhook{id: s.newID(), body: body})
		reply(w, 201, nil)
	default:
		id, _ := strconv.ParseInt(parts[4], 10, 64)
		for i, hook := range project.webhooks {
			if hook.id != id {
				continue
			}
			if r.Method == http.MethodPut {
				project.webhooks[i].body = body
			} else {
				project.webhooks = append(project.webhooks[:i], project.webhooks[i+1:]...)
			}
			reply(w, 200, nil)
			return
		}
		reply(w, 404, nil)
	}
}

func (s *harborState) quotaRoutes(w http.ResponseWriter, r *http.Request, parts []string, body map[string]any) {
	if r.Method == http.MethodGet {
		id, _ := strconv.ParseInt(r.URL.Query().Get("reference_id"), 10, 64)
		reply(w, 200, []map[string]any{{"id": id}})
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	hard, _ := body["hard"].(map[string]any)
	limit, _ := hard["storage"].(float64)
	for _, project := range s.projects {
		if project.id == id {
			project.quota = int64(limit)
		}
	}
	reply(w, 200, nil)
}

func (s *harborState) retentionRoutes(w http.ResponseWriter, r *http.Request, parts []string, body map[string]any) {
	switch {
	case r.Method == http.MethodPost:
		scope, _ := body["scope"].(map[string]any)
		ref, _ := scope["ref"].(float64)
		id := s.newID()
		body["id"] = id
		s.retentions[id] = body
		for _, project := range s.projects {
			if project.id == int64(ref) {
				project.retentionID = id
			}
		}
		reply(w, 201, nil)
	case r.Method == http.MethodPut:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		s.retentions[id] = body
		reply(w, 200, nil)
	default:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		reply(w, 200, s.retentions[id])
	}
}

func (s *harborState) robotRoutes(w http.ResponseWriter, r *http.Request, parts []string, body map[string]any) {
	switch {
	case r.Method == http.MethodGet:
		wanted := strings.TrimPrefix(r.URL.Query().Get("q"), "Level=project,ProjectID=")
		out := []map[string]any{}
		for name, project := range s.projects {
			if strconv.FormatInt(project.id, 10) != wanted {
				continue
			}
			for _, robot := range project.robots {
				out = append(out, map[string]any{"id": robot.id, "name": "robot$" + name + "+" + robot.name})
			}
		}
		reply(w, 200, out)
	case r.Method == http.MethodPost:
		s.createRobot(w, body)
	case r.Method == http.MethodPatch:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		secret, _ := body["secret"].(string)
		if !s.setRobotSecret(id, secret) {
			reply(w, 404, nil)
			return
		}
		reply(w, 200, nil)
	case r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		for _, project := range s.projects {
			for i, robot := range project.robots {
				if robot.id == id {
					project.robots = append(project.robots[:i], project.robots[i+1:]...)
				}
			}
		}
		reply(w, 200, nil)
	}
}

func (s *harborState) createRobot(w http.ResponseWriter, body map[string]any) {
	permissions, _ := body["permissions"].([]any)
	grant, _ := permissions[0].(map[string]any)
	projectName, _ := grant["namespace"].(string)
	name, _ := body["name"].(string)
	project := s.projects[projectName]
	if project == nil {
		reply(w, 400, map[string]any{"errors": []map[string]any{{"message": "no project"}}})
		return
	}
	for _, robot := range project.robots {
		if robot.name == name {
			reply(w, 409, map[string]any{"errors": []map[string]any{{"message": "exists"}}})
			return
		}
	}
	s.secretSeq++
	robot := fakeRobot{id: s.newID(), name: name, secret: "secret-" + strconv.Itoa(s.secretSeq), access: grant["access"].([]any)}
	project.robots = append(project.robots, robot)
	reply(w, 201, map[string]any{"id": robot.id, "name": "robot$" + projectName + "+" + name, "secret": robot.secret})
}

func (s *harborState) setRobotSecret(id int64, secret string) bool {
	for _, project := range s.projects {
		for i, robot := range project.robots {
			if robot.id == id {
				project.robots[i].secret = secret
				s.robotSets = append(s.robotSets, strconv.FormatInt(id, 10))
				return true
			}
		}
	}
	return false
}
