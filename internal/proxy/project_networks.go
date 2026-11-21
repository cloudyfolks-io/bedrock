package proxy

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

const networkInventoryTimeout = 30 * time.Second

var (
	errNetworkRulesIncomplete = errors.New("the API server did not return complete network access rules")
	errNetworkInventoryLimit  = errors.New("the project network inventory exceeds the list limit")
)

func ProjectNetworksHandler(upstream *Upstream, privileged client.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || caller.Token == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		name := r.PathValue("project")
		if len(validation.IsDNS1123Label(name)) != 0 {
			httpjson.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), networkInventoryTimeout)
		defer cancel()
		var projects v1alpha1.ProjectList
		if err := privileged.List(ctx, &projects, client.MatchingFields{"metadata.name": name}); err != nil {
			upstreamFailure(w, r, "read the project for network inventory", err)
			return
		}
		if len(projects.Items) != 1 || projects.Items[0].Name != name || projects.Items[0].Spec.TenantRef == "" {
			httpjson.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		project := projects.Items[0]
		namespace := projectListNamespace(project)
		if namespace == "" {
			httpjson.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		config := upstream.RESTConfig(caller.Token)
		reviews, err := kubernetes.NewForConfig(config)
		if err != nil {
			upstreamFailure(w, r, "build the project access client", err)
			return
		}
		access, err := reviewAll(ctx, reviews, []authorizationv1.ResourceAttributes{listProjectsAttributes(), projectProbe(namespace)})
		if err != nil {
			upstreamFailure(w, r, "review access to the network project", err)
			return
		}
		if !access[0].Allowed && !access[1].Allowed {
			httpjson.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		reader, err := client.New(config, client.Options{Scheme: privileged.Scheme(), Mapper: privileged.RESTMapper()})
		if err != nil {
			upstreamFailure(w, r, "build the network inventory client", err)
			return
		}
		networks, err := projectNetworks(ctx, reader, reviews, project.Spec.TenantRef, namespace)
		if errors.Is(err, errNetworkInventoryLimit) {
			httpjson.WriteError(w, http.StatusUnprocessableEntity, "network_inventory_limit")
			return
		}
		if apierrors.IsForbidden(err) {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
		if err != nil {
			upstreamFailure(w, r, "read the network inventory", err)
			return
		}
		httpjson.Write(w, http.StatusOK, v1alpha1.NetworkList{
			TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "NetworkList"},
			Items:    networks,
		})
	})
}

func projectNetworks(ctx context.Context, reader client.Reader, reviews kubernetes.Interface, tenant, namespace string) ([]v1alpha1.Network, error) {
	items, listErr := listTenantNetworks(ctx, reader, tenant)
	if !apierrors.IsForbidden(listErr) {
		return items, listErr
	}
	review, err := reviews.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx,
		&authorizationv1.SelfSubjectRulesReview{Spec: authorizationv1.SelfSubjectRulesReviewSpec{Namespace: namespace}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if review.Status.Incomplete || review.Status.EvaluationError != "" {
		return nil, errNetworkRulesIncomplete
	}
	items = []v1alpha1.Network{}
	allowed := false
	names := networkReadNames(review.Status.ResourceRules)
	if len(names) > projectPage {
		return nil, errNetworkInventoryLimit
	}
	for _, name := range names {
		var network v1alpha1.Network
		err := reader.Get(ctx, client.ObjectKey{Name: name}, &network)
		switch {
		case apierrors.IsForbidden(err):
			continue
		case apierrors.IsNotFound(err):
			allowed = true
			continue
		case err != nil:
			return nil, err
		}
		allowed = true
		if network.Spec.TenantRef == tenant {
			items = append(items, network)
		}
	}
	if !allowed {
		return nil, listErr
	}
	return items, nil
}

func listTenantNetworks(ctx context.Context, reader client.Reader, tenant string) ([]v1alpha1.Network, error) {
	items := []v1alpha1.Network{}
	for cursor := ""; ; {
		var page v1alpha1.NetworkList
		if err := reader.List(ctx, &page, client.Limit(projectPage), client.Continue(cursor)); err != nil {
			return nil, err
		}
		for _, network := range page.Items {
			if network.Spec.TenantRef == tenant {
				items = append(items, network)
			}
		}
		if len(items) > projectPage {
			return nil, errNetworkInventoryLimit
		}
		if page.Continue == "" {
			return items, nil
		}
		cursor = page.Continue
	}
}

func networkReadNames(rules []authorizationv1.ResourceRule) []string {
	var names []string
	for _, rule := range rules {
		if (slices.Contains(rule.Verbs, "get") || slices.Contains(rule.Verbs, "*")) &&
			(slices.Contains(rule.APIGroups, v1alpha1.GroupVersion.Group) || slices.Contains(rule.APIGroups, "*")) &&
			(slices.Contains(rule.Resources, "networks") || slices.Contains(rule.Resources, "*")) {
			for _, name := range rule.ResourceNames {
				if len(validation.IsDNS1123Label(name)) == 0 {
					names = append(names, name)
				}
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}
