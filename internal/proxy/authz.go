package proxy

import (
	"context"
	"net/http"
	"sort"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

const (
	maxChecks      = 100
	maxCheckString = 253
	reviewParallel = 8
	maxProbed      = 500
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
		var projects v1alpha1.ProjectList
		if err := privileged.List(r.Context(), &projects); err != nil {
			upstreamFailure(w, r, "list projects", err)
			return
		}
		visible, err := visibleProjects(r.Context(), reviews, projects.Items)
		if err != nil {
			upstreamFailure(w, r, "probe the projects", err)
			return
		}
		httpjson.Write(w, http.StatusOK, map[string]any{"projects": visible})
	})
}

func visibleProjects(ctx context.Context, reviews kubernetes.Interface, projects []v1alpha1.Project) ([]projectView, error) {
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	listAll, err := reviewAll(ctx, reviews, []authorizationv1.ResourceAttributes{listProjectsAttributes()})
	if err != nil {
		return nil, err
	}
	if listAll[0].Allowed {
		return viewsOf(projects), nil
	}
	probed := projects[:min(len(projects), maxProbed)]
	namespaced := make([]v1alpha1.Project, 0, len(probed))
	attributes := make([]authorizationv1.ResourceAttributes, 0, len(probed))
	for _, project := range probed {
		if project.Status.Namespace != "" {
			namespaced = append(namespaced, project)
			attributes = append(attributes, projectProbe(project.Status.Namespace))
		}
	}
	results, err := reviewAll(ctx, reviews, attributes)
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
