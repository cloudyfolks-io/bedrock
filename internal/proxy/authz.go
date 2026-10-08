package proxy

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const (
	maxChecks      = 100
	maxCheckString = 253
	reviewParallel = 8
	projectPage    = 500
	reviewCacheTTL = 30 * time.Second
	reviewCacheMax = 20000
)

var allowedVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}

type accessCheck struct {
	Verb        string `json:"verb"`
	Group       string `json:"group"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
}

type authzRequest struct {
	Checks []accessCheck `json:"checks"`
}

type accessResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type projectView struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Tenant      string `json:"tenant"`
	Namespace   string `json:"namespace"`
}

func AuthzHandler(u *Upstream) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || r.Method != http.MethodPost {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		request, err := httpjson.Decode[authzRequest](w, r)
		if err != nil || !validChecks(request.Checks) {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		reviews, err := kubernetes.NewForConfig(u.RESTConfig(caller.Token))
		if err != nil {
			upstreamFailure(w, r, "build the review client", err)
			return
		}
		results, err := reviewAll(r.Context(), reviews, attributesOf(request.Checks))
		if err != nil {
			upstreamFailure(w, r, "review the access checks", err)
			return
		}
		httpjson.Write(w, http.StatusOK, map[string]any{"results": results})
	})
}

func ProjectsHandler(u *Upstream, privileged client.Reader) http.Handler {
	cache := newReviewCache(time.Now)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || r.Method != http.MethodGet {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		reviews, err := kubernetes.NewForConfig(u.RESTConfig(caller.Token))
		if err != nil {
			upstreamFailure(w, r, "build the review client", err)
			return
		}
		projects, err := listAllProjects(r.Context(), privileged)
		if err != nil {
			upstreamFailure(w, r, "list projects", err)
			return
		}
		visible, err := visibleProjects(r.Context(), cache.forToken(reviews, caller.Token), projects)
		if err != nil {
			upstreamFailure(w, r, "probe the projects", err)
			return
		}
		httpjson.Write(w, http.StatusOK, map[string]any{"projects": visible})
	})
}

func listAllProjects(ctx context.Context, reader client.Reader) ([]v1alpha1.Project, error) {
	var all []v1alpha1.Project
	options := []client.ListOption{client.Limit(projectPage)}
	for {
		var page v1alpha1.ProjectList
		if err := reader.List(ctx, &page, options...); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.Continue == "" {
			return all, nil
		}
		options = []client.ListOption{client.Limit(projectPage), client.Continue(page.Continue)}
	}
}

func visibleProjects(ctx context.Context, reviews reviewer, projects []v1alpha1.Project) ([]projectView, error) {
	projects = slices.Clone(projects)
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	listAll, err := reviews.all(ctx, []authorizationv1.ResourceAttributes{listProjectsAttributes()})
	if err != nil {
		return nil, err
	}
	if listAll[0].Allowed {
		return viewsOf(projects), nil
	}
	namespaced := make([]v1alpha1.Project, 0, len(projects))
	attributes := make([]authorizationv1.ResourceAttributes, 0, len(projects))
	for _, project := range projects {
		if project.Status.Namespace != "" {
			namespaced = append(namespaced, project)
			attributes = append(attributes, projectProbe(project.Status.Namespace))
		}
	}
	results, err := reviews.all(ctx, attributes)
	if err != nil {
		return nil, err
	}
	allowed := make([]v1alpha1.Project, 0, len(namespaced))
	for i, result := range results {
		if result.Allowed {
			allowed = append(allowed, namespaced[i])
		}
	}
	return viewsOf(allowed), nil
}

func viewsOf(projects []v1alpha1.Project) []projectView {
	views := make([]projectView, 0, len(projects))
	for _, project := range projects {
		views = append(views, projectView{
			Name:        project.Name,
			DisplayName: project.Spec.DisplayName,
			Tenant:      project.Spec.TenantRef,
			Namespace:   project.Status.Namespace,
		})
	}
	return views
}

func listProjectsAttributes() authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{Verb: "list", Group: v1alpha1.GroupVersion.Group, Resource: "projects"}
}

func projectProbe(namespace string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{Verb: "list", Group: v1alpha1.GroupVersion.Group, Resource: "virtualmachines", Namespace: namespace}
}

func validChecks(checks []accessCheck) bool {
	if len(checks) > maxChecks {
		return false
	}
	for _, check := range checks {
		if check.Resource == "" || !isOneOf(check.Verb, allowedVerbs...) {
			return false
		}
		for _, text := range []string{check.Verb, check.Group, check.Resource, check.Subresource, check.Namespace, check.Name} {
			if len(text) > maxCheckString {
				return false
			}
		}
	}
	return true
}

func attributesOf(checks []accessCheck) []authorizationv1.ResourceAttributes {
	attributes := make([]authorizationv1.ResourceAttributes, 0, len(checks))
	for _, check := range checks {
		attributes = append(attributes, authorizationv1.ResourceAttributes{
			Verb: check.Verb, Group: check.Group, Resource: check.Resource,
			Subresource: check.Subresource, Namespace: check.Namespace, Name: check.Name,
		})
	}
	return attributes
}

func reviewAll(ctx context.Context, reviews kubernetes.Interface, attributes []authorizationv1.ResourceAttributes) ([]accessResult, error) {
	results := make([]accessResult, len(attributes))
	slots := make(chan struct{}, reviewParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := range attributes {
		slots <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			result, err := reviewOne(ctx, reviews, attributes[i])
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			results[i] = result
		}(i)
	}
	wg.Wait()
	return results, firstErr
}

type reviewer interface {
	all(ctx context.Context, attributes []authorizationv1.ResourceAttributes) ([]accessResult, error)
}

type reviewEntry struct {
	result  accessResult
	expires time.Time
}

type reviewCache struct {
	clock   func() time.Time
	mu      sync.Mutex
	entries map[string]reviewEntry
}

func newReviewCache(clock func() time.Time) *reviewCache {
	return &reviewCache{clock: clock, entries: map[string]reviewEntry{}}
}

type cachedReviewer struct {
	cache    *reviewCache
	reviews  kubernetes.Interface
	tokenKey string
}

func (c *reviewCache) forToken(reviews kubernetes.Interface, token string) cachedReviewer {
	return cachedReviewer{cache: c, reviews: reviews, tokenKey: secret.SHA256Hex(token)}
}

func (r cachedReviewer) all(ctx context.Context, attributes []authorizationv1.ResourceAttributes) ([]accessResult, error) {
	results := make([]accessResult, len(attributes))
	var missing []authorizationv1.ResourceAttributes
	var missingAt []int
	for i, attribute := range attributes {
		if cached, found := r.cache.get(r.key(attribute)); found {
			results[i] = cached
			continue
		}
		missing = append(missing, attribute)
		missingAt = append(missingAt, i)
	}
	fresh, err := reviewAll(ctx, r.reviews, missing)
	if err != nil {
		return nil, err
	}
	for j, result := range fresh {
		results[missingAt[j]] = result
		r.cache.put(r.key(missing[j]), result)
	}
	return results, nil
}

func (r cachedReviewer) key(attribute authorizationv1.ResourceAttributes) string {
	return fmt.Sprintf("%s|%v", r.tokenKey, attribute)
}

func (c *reviewCache) get(key string) (accessResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found || !c.clock().Before(entry.expires) {
		return accessResult{}, false
	}
	return entry.result, true
}

func (c *reviewCache) put(key string, result accessResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= reviewCacheMax {
		now := c.clock()
		for existing, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, existing)
			}
		}
	}
	if len(c.entries) < reviewCacheMax {
		c.entries[key] = reviewEntry{result: result, expires: c.clock().Add(reviewCacheTTL)}
	}
}
