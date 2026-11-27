package harbor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiRoot        = "/api/v2.0"
	bytesPerGB     = int64(1024 * 1024 * 1024)
	unlimitedBytes = int64(-1)
	maxAnswerBytes = 1 << 20
	maxMessage     = 200
	requestTimeout = 30 * time.Second
	pageSize       = 100
	maxRobotPages  = 1000
	webhookPolicy  = "platform-events"
)

var (
	ErrConflict     = errors.New("harbor: conflict")
	ErrNotFound     = errors.New("harbor: not found")
	ErrUnavailable  = errors.New("harbor: unavailable")
	ErrTypeMismatch = errors.New("harbor: registry endpoint has another type")
)

type Client struct {
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
}

type Project struct {
	ID          int64
	Name        string
	RetentionID int64
	Created     time.Time
}

type Summary struct {
	UsedBytes       int64
	HardBytes       int64
	RepositoryCount int64
}

type Robot struct {
	ID      int64
	Name    string
	Created time.Time
}

type Repository struct {
	Name string `json:"name"`
}

type request struct {
	method  string
	path    string
	body    any
	byName  bool
	secrets []string
}

type answer struct {
	status int
	body   []byte
	header http.Header
}

func (c *Client) CreateProject(ctx context.Context, name string, quotaGB int64) error {
	body := map[string]any{
		"project_name":  name,
		"storage_limit": QuotaBytes(quotaGB),
		"metadata":      map[string]string{"public": "false"},
	}
	_, err := c.send(ctx, request{method: http.MethodPost, path: "/projects", body: body})
	return err
}

func (c *Client) GetProject(ctx context.Context, name string) (Project, error) {
	got, err := c.send(ctx, request{method: http.MethodGet, path: "/projects/" + name, byName: true})
	if err != nil {
		return Project{}, err
	}
	var decoded struct {
		ProjectID    int64     `json:"project_id"`
		Name         string    `json:"name"`
		CreationTime time.Time `json:"creation_time"`
		Metadata     struct {
			RetentionID string `json:"retention_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		return Project{}, fmt.Errorf("harbor: read project %q: answer is not valid", name)
	}
	retentionID, _ := strconv.ParseInt(decoded.Metadata.RetentionID, 10, 64)
	return Project{ID: decoded.ProjectID, Name: decoded.Name, RetentionID: retentionID, Created: decoded.CreationTime}, nil
}

func (c *Client) DeleteProject(ctx context.Context, name string) error {
	_, err := c.send(ctx, request{method: http.MethodDelete, path: "/projects/" + name, byName: true})
	return ignoreNotFound(err)
}

func (c *Client) ProjectSummary(ctx context.Context, name string) (Summary, error) {
	got, err := c.send(ctx, request{method: http.MethodGet, path: "/projects/" + name + "/summary", byName: true})
	if err != nil {
		return Summary{}, err
	}
	var decoded struct {
		RepoCount int64 `json:"repo_count"`
		Quota     struct {
			Hard map[string]int64 `json:"hard"`
			Used map[string]int64 `json:"used"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		return Summary{}, fmt.Errorf("harbor: read summary of %q: answer is not valid", name)
	}
	return Summary{UsedBytes: decoded.Quota.Used["storage"], HardBytes: decoded.Quota.Hard["storage"], RepositoryCount: decoded.RepoCount}, nil
}

func (c *Client) SetQuota(ctx context.Context, name string, quotaGB int64) error {
	project, err := c.GetProject(ctx, name)
	if err != nil {
		return err
	}
	path := "/quotas?reference=project&reference_id=" + strconv.FormatInt(project.ID, 10)
	got, err := c.send(ctx, request{method: http.MethodGet, path: path})
	if err != nil {
		return err
	}
	var rows []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(got.body, &rows); err != nil {
		return fmt.Errorf("harbor: read quotas of %q: answer is not valid", name)
	}
	if len(rows) == 0 {
		return fmt.Errorf("harbor: project %q has no quota row", name)
	}
	body := map[string]any{"hard": map[string]int64{"storage": QuotaBytes(quotaGB)}}
	_, err = c.send(ctx, request{method: http.MethodPut, path: "/quotas/" + strconv.FormatInt(rows[0].ID, 10), body: body})
	return err
}

func (c *Client) CreateRobot(ctx context.Context, project, name, permission string) (Robot, string, error) {
	access, err := AccessFor(permission)
	if err != nil {
		return Robot{}, "", err
	}
	body := map[string]any{
		"name":     name,
		"level":    "project",
		"duration": -1,
		"permissions": []map[string]any{
			{"kind": "project", "namespace": project, "access": access},
		},
	}
	got, err := c.send(ctx, request{method: http.MethodPost, path: "/robots", body: body})
	if err != nil {
		return Robot{}, "", err
	}
	var decoded struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		return Robot{}, "", fmt.Errorf("harbor: read robot %q: answer is not valid", name)
	}
	return Robot{ID: decoded.ID, Name: decoded.Name}, decoded.Secret, nil
}

type robotRow struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	CreationTime time.Time `json:"creation_time"`
	Permissions  []struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
	} `json:"permissions"`
}

func (r robotRow) belongsTo(project string) bool {
	for _, permission := range r.Permissions {
		if permission.Kind == "project" && permission.Namespace == project {
			return true
		}
	}
	return false
}

func (c *Client) ListRobots(ctx context.Context, project string) ([]Robot, error) {
	owner, err := c.GetProject(ctx, project)
	if err != nil {
		return nil, err
	}
	var robots []Robot
	for page := 1; page <= maxRobotPages; page++ {
		rows, err := c.robotPage(ctx, project, owner.ID, page)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.belongsTo(project) {
				robots = append(robots, Robot{ID: row.ID, Name: row.Name, Created: row.CreationTime})
			}
		}
		if len(rows) < pageSize {
			return robots, nil
		}
	}
	return nil, fmt.Errorf("harbor: project %q has more than %d robots", project, maxRobotPages*pageSize)
}

func (c *Client) robotPage(ctx context.Context, project string, projectID int64, page int) ([]robotRow, error) {
	query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}, "q": {"Level=project,ProjectID=" + strconv.FormatInt(projectID, 10)}}
	got, err := c.send(ctx, request{method: http.MethodGet, path: "/robots?" + query.Encode()})
	if err != nil {
		return nil, err
	}
	var rows []robotRow
	if err := json.Unmarshal(got.body, &rows); err != nil {
		return nil, fmt.Errorf("harbor: read robots of %q: answer is not valid", project)
	}
	return rows, nil
}

func (c *Client) FindRobot(ctx context.Context, project, name string) (Robot, bool, error) {
	robots, err := c.ListRobots(ctx, project)
	if err != nil {
		return Robot{}, false, err
	}
	for _, robot := range robots {
		if strings.HasSuffix(robot.Name, "+"+name) {
			return robot, true, nil
		}
	}
	return Robot{}, false, nil
}

func (c *Client) SetRobotSecret(ctx context.Context, id int64, secret string) error {
	_, err := c.send(ctx, request{method: http.MethodPatch, path: "/robots/" + strconv.FormatInt(id, 10), body: map[string]string{"secret": secret}, secrets: []string{secret}})
	return err
}

func (c *Client) DeleteRobot(ctx context.Context, id int64) error {
	_, err := c.send(ctx, request{method: http.MethodDelete, path: "/robots/" + strconv.FormatInt(id, 10)})
	return ignoreNotFound(err)
}

func (c *Client) ListRepositories(ctx context.Context, project string, page, size int) ([]Repository, int64, error) {
	path := fmt.Sprintf("/projects/%s/repositories?page=%d&page_size=%d", project, page, size)
	got, err := c.send(ctx, request{method: http.MethodGet, path: path, byName: true})
	if err != nil {
		return nil, 0, err
	}
	var repositories []Repository
	if err := json.Unmarshal(got.body, &repositories); err != nil {
		return nil, 0, fmt.Errorf("harbor: read repositories of %q: answer is not valid", project)
	}
	total, _ := strconv.ParseInt(got.header.Get("X-Total-Count"), 10, 64)
	return repositories, max(total, int64(len(repositories))), nil
}

func (c *Client) DeleteRepository(ctx context.Context, project, repository string) error {
	path := "/projects/" + project + "/repositories/" + url.PathEscape(url.PathEscape(repository))
	_, err := c.send(ctx, request{method: http.MethodDelete, path: path, byName: true})
	return ignoreNotFound(err)
}

func (c *Client) GetRetention(ctx context.Context, project string) (*Retention, error) {
	owner, err := c.GetProject(ctx, project)
	if err != nil || owner.RetentionID == 0 {
		return nil, err
	}
	got, err := c.send(ctx, request{method: http.MethodGet, path: "/retentions/" + strconv.FormatInt(owner.RetentionID, 10)})
	if err != nil {
		return nil, err
	}
	var retention Retention
	if err := json.Unmarshal(got.body, &retention); err != nil {
		return nil, fmt.Errorf("harbor: read retention of %q: answer is not valid", project)
	}
	return &retention, nil
}

func (c *Client) SetRetention(ctx context.Context, project string, retention Retention) error {
	owner, err := c.GetProject(ctx, project)
	if err != nil {
		return err
	}
	retention.Scope = &RetentionScope{Level: "project", Ref: owner.ID}
	method, path := http.MethodPost, "/retentions"
	if owner.RetentionID != 0 {
		retention.ID = owner.RetentionID
		method, path = http.MethodPut, "/retentions/"+strconv.FormatInt(owner.RetentionID, 10)
	}
	_, err = c.send(ctx, request{method: method, path: path, body: retention})
	return err
}

type webhookPolicyRow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (c *Client) listWebhooks(ctx context.Context, project string) ([]webhookPolicyRow, error) {
	path := fmt.Sprintf("/projects/%s/webhook/policies?page=1&page_size=%d", project, pageSize)
	got, err := c.send(ctx, request{method: http.MethodGet, path: path, byName: true})
	if err != nil {
		return nil, err
	}
	var policies []webhookPolicyRow
	if err := json.Unmarshal(got.body, &policies); err != nil {
		return nil, fmt.Errorf("harbor: read webhooks of %q: answer is not valid", project)
	}
	return policies, nil
}

func (c *Client) EnsureWebhook(ctx context.Context, project, address, authHeader string) error {
	base := "/projects/" + project + "/webhook/policies"
	policies, err := c.listWebhooks(ctx, project)
	if err != nil {
		return err
	}
	body := map[string]any{
		"name":    webhookPolicy,
		"enabled": true,
		"targets": []map[string]any{
			{"type": "http", "address": address, "auth_header": authHeader, "skip_cert_verify": false},
		},
		"event_types": []string{"PUSH_ARTIFACT", "DELETE_ARTIFACT"},
	}
	method, path := http.MethodPost, base
	for _, policy := range policies {
		if policy.Name == webhookPolicy {
			method, path = http.MethodPut, base+"/"+strconv.FormatInt(policy.ID, 10)
		}
	}
	_, err = c.send(ctx, request{method: method, path: path, body: body, byName: true, secrets: []string{authHeader}})
	return err
}

func (c *Client) DeleteWebhook(ctx context.Context, project string) error {
	policies, err := c.listWebhooks(ctx, project)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if policy.Name == webhookPolicy {
			_, err := c.send(ctx, request{method: http.MethodDelete, path: "/projects/" + project + "/webhook/policies/" + strconv.FormatInt(policy.ID, 10), byName: true})
			return ignoreNotFound(err)
		}
	}
	return nil
}

func (c *Client) SetConfigurations(ctx context.Context, configuration map[string]any) error {
	_, err := c.send(ctx, request{method: http.MethodPut, path: "/configurations", body: configuration})
	return err
}

func (c *Client) ScheduleGC(ctx context.Context, cron string) error {
	body := map[string]any{
		"schedule":   map[string]string{"type": "Custom", "cron": cron},
		"parameters": map[string]any{"delete_untagged": false},
	}
	_, err := c.send(ctx, request{method: http.MethodPost, path: "/system/gc/schedule", body: body})
	if errors.Is(err, ErrConflict) {
		_, err = c.send(ctx, request{method: http.MethodPut, path: "/system/gc/schedule", body: body})
	}
	return err
}

func (c *Client) EnsureRegistryEndpoint(ctx context.Context, name, adapterType, endpoint string) (int64, error) {
	body := map[string]any{"name": name, "type": adapterType, "url": endpoint, "insecure": false}
	got, err := c.send(ctx, request{method: http.MethodPost, path: "/registries", body: body})
	if err == nil {
		return idFromLocation(got.header.Get("Location")), nil
	}
	if !errors.Is(err, ErrConflict) {
		return 0, err
	}
	existing, err := c.findRegistryEndpoint(ctx, name)
	if err != nil {
		return 0, err
	}
	if existing.Type != adapterType {
		return 0, fmt.Errorf("%w: %q is type %q and the setting asks for type %q; use a new name", ErrTypeMismatch, name, existing.Type, adapterType)
	}
	return existing.ID, nil
}

type registryEndpoint struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (c *Client) findRegistryEndpoint(ctx context.Context, name string) (registryEndpoint, error) {
	query := url.Values{"page": {"1"}, "page_size": {strconv.Itoa(pageSize)}, "q": {"name=" + name}}
	got, err := c.send(ctx, request{method: http.MethodGet, path: "/registries?" + query.Encode()})
	if err != nil {
		return registryEndpoint{}, err
	}
	var endpoints []registryEndpoint
	if err := json.Unmarshal(got.body, &endpoints); err != nil {
		return registryEndpoint{}, fmt.Errorf("harbor: read registry endpoints: answer is not valid")
	}
	for _, endpoint := range endpoints {
		if endpoint.Name == name {
			return endpoint, nil
		}
	}
	return registryEndpoint{}, fmt.Errorf("harbor: registry endpoint %q: %w", name, ErrNotFound)
}

func (c *Client) EnsureProxyProject(ctx context.Context, name string, registryID int64) error {
	body := map[string]any{
		"project_name":  name,
		"registry_id":   registryID,
		"storage_limit": unlimitedBytes,
		"metadata":      map[string]string{"public": "true"},
	}
	_, err := c.send(ctx, request{method: http.MethodPost, path: "/projects", body: body})
	if errors.Is(err, ErrConflict) {
		return nil
	}
	return err
}

func (c *Client) Health(ctx context.Context) error {
	got, err := c.send(ctx, request{method: http.MethodGet, path: "/health"})
	if err != nil {
		return err
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(got.body, &health); err != nil || health.Status != "healthy" {
		return fmt.Errorf("%w: health status is not healthy", ErrUnavailable)
	}
	return nil
}

func (c *Client) send(ctx context.Context, in request) (answer, error) {
	reader, err := bodyReader(in.body)
	if err != nil {
		return answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, in.method, c.BaseURL+apiRoot+in.path, reader)
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if in.byName {
		req.Header.Set("X-Is-Resource-Name", "true")
	}
	req.SetBasicAuth(c.Username, c.Password)
	resp, err := c.client().Do(req)
	if err != nil {
		return answer{}, fmt.Errorf("harbor %s %s: %w: request failed", in.method, pathOf(in.path), ErrUnavailable)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	if err != nil {
		return answer{}, fmt.Errorf("harbor %s %s: %w: answer could not be read", in.method, pathOf(in.path), ErrUnavailable)
	}
	if len(raw) > maxAnswerBytes {
		return answer{}, fmt.Errorf("harbor %s %s: answer is larger than %d bytes", in.method, pathOf(in.path), maxAnswerBytes)
	}
	got := answer{status: resp.StatusCode, body: raw, header: resp.Header}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return got, nil
	}
	return answer{}, c.statusError(in, got)
}

func (c *Client) statusError(in request, got answer) error {
	message := redact(harborMessage(got.body), append([]string{c.Password}, in.secrets...))
	base := fmt.Errorf("harbor %s %s: status %d: %s", in.method, pathOf(in.path), got.status, message)
	switch {
	case got.status == http.StatusConflict:
		return fmt.Errorf("%w: %w", base, ErrConflict)
	case got.status == http.StatusNotFound:
		return fmt.Errorf("%w: %w", base, ErrNotFound)
	case got.status >= 500:
		return fmt.Errorf("%w: %w", base, ErrUnavailable)
	}
	return base
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

func bodyReader(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(raw), nil
}

func pathOf(path string) string {
	return strings.SplitN(path, "?", 2)[0]
}

func harborMessage(raw []byte) string {
	var decoded struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &decoded) != nil || len(decoded.Errors) == 0 {
		return "no message"
	}
	message := decoded.Errors[0].Message
	if len(message) > maxMessage {
		return message[:maxMessage]
	}
	return message
}

func redact(message string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func ignoreNotFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func QuotaBytes(quotaGB int64) int64 {
	if quotaGB <= 0 {
		return unlimitedBytes
	}
	return quotaGB * bytesPerGB
}

func idFromLocation(location string) int64 {
	id, _ := strconv.ParseInt(location[strings.LastIndex(location, "/")+1:], 10, 64)
	return id
}
