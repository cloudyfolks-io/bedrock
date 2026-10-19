package proxy

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const (
	bedrockAPIPrefix  = "/apis/bedrock.cloudyfolks.io/v1alpha1/"
	identityCacheLife = 30 * time.Second
	maxSubSegments    = 2
)

type User struct {
	Name   string
	Groups []string
}

type userKey struct{}

func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userKey{}).(User)
	return user, ok
}

type subresourceTarget struct {
	plural      string
	subresource string
	key         client.ObjectKey
}

type identityEntry struct {
	user    User
	expires time.Time
}

type identityCache struct {
	mu      sync.Mutex
	entries map[string]identityEntry
}

func SubresourceRouter(subs *Subresources, u *Upstream, privileged client.Client, next http.Handler) http.Handler {
	identities := &identityCache{entries: map[string]identityEntry{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, found := parseSubresourcePath(r.URL.Path, r.URL.RawPath)
		if !found || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		handler, registered := subs.Lookup(target.plural, target.subresource)
		if !registered {
			next.ServeHTTP(w, r)
			return
		}
		caller, authenticated := CallerFrom(r.Context())
		if !authenticated || caller.Token == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		reviews, err := kubernetes.NewForConfig(u.RESTConfig(caller.Token))
		if err != nil {
			upstreamFailure(w, r, "build the review client", err)
			return
		}
		allowed, err := reviewAccess(r.Context(), reviews, subresourceAttributes(target, r.Method))
		if err != nil {
			upstreamFailure(w, r, "review the access", err)
			return
		}
		if !allowed {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
		user, err := identities.userOf(r.Context(), reviews, caller)
		if err != nil {
			upstreamFailure(w, r, "review the identity", err)
			return
		}
		handler(r.Context(), privileged, target.key, w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func SubresourceVerb(plural, subresource, method string) string {
	switch {
	case plural == "virtualmachines" && method == http.MethodPost && isOneOf(subresource, "start", "stop", "restart", "migrate", "snapshot"):
		return "update"
	case plural == "virtualmachines" && isOneOf(subresource, "snapshot", "console/vnc", "console/serial") && method == http.MethodGet:
		return "get"
	case plural == "images" && subresource == "upload":
		return "create"
	case plural == "containers" && subresource == "restart" && method == http.MethodPost:
		return "update"
	case plural == "containers" && isOneOf(subresource, "logs", "exec") && method == http.MethodGet:
		return "get"
	}
	return verbOfMethod(method)
}

func verbOfMethod(method string) string {
	switch method {
	case http.MethodGet:
		return "get"
	case http.MethodDelete:
		return "delete"
	}
	return "update"
}

func isOneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func subresourceAttributes(target subresourceTarget, method string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{
		Namespace:   target.key.Namespace,
		Verb:        SubresourceVerb(target.plural, target.subresource, method),
		Group:       v1alpha1.GroupVersion.Group,
		Resource:    target.plural,
		Subresource: target.subresource,
		Name:        target.key.Name,
	}
}

func parseSubresourcePath(path, rawPath string) (subresourceTarget, bool) {
	if !strings.HasPrefix(path, bedrockAPIPrefix) || strings.Contains(strings.ToLower(rawPath), "%2f") {
		return subresourceTarget{}, false
	}
	segments := strings.Split(strings.TrimPrefix(path, bedrockAPIPrefix), "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return subresourceTarget{}, false
		}
	}
	namespace := ""
	if segments[0] == "namespaces" {
		if len(segments) < 2 {
			return subresourceTarget{}, false
		}
		namespace, segments = segments[1], segments[2:]
	}
	if len(segments) < 3 || len(segments)-2 > maxSubSegments {
		return subresourceTarget{}, false
	}
	return subresourceTarget{
		plural:      segments[0],
		subresource: strings.Join(segments[2:], "/"),
		key:         client.ObjectKey{Namespace: namespace, Name: segments[1]},
	}, true
}

func reviewAccess(ctx context.Context, reviews kubernetes.Interface, attributes authorizationv1.ResourceAttributes) (bool, error) {
	result, err := reviewOne(ctx, reviews, attributes)
	return result.Allowed, err
}

func reviewOne(ctx context.Context, reviews kubernetes.Interface, attributes authorizationv1.ResourceAttributes) (accessResult, error) {
	review := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attributes}}
	answer, err := reviews.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return accessResult{}, err
	}
	return accessResult{Allowed: answer.Status.Allowed, Reason: answer.Status.Reason}, nil
}

func (c *identityCache) userOf(ctx context.Context, reviews kubernetes.Interface, caller Caller) (User, error) {
	if caller.Principal != nil {
		return authnUser(*caller.Principal), nil
	}
	key := secret.SHA256Hex(caller.Token)
	c.mu.Lock()
	cached, found := c.entries[key]
	c.mu.Unlock()
	if found && time.Now().Before(cached.expires) {
		return cached.user, nil
	}
	answer, err := reviews.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return User{}, err
	}
	user := User{Name: answer.Status.UserInfo.Username, Groups: answer.Status.UserInfo.Groups}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = identityEntry{user: user, expires: time.Now().Add(identityCacheLife)}
	if len(c.entries) > cachePruneAbove {
		c.prune()
	}
	return user, nil
}

func authnUser(principal Principal) User {
	groups := make([]string, 0, len(principal.Groups))
	for _, group := range principal.Groups {
		groups = append(groups, v1alpha1.AuthnPrefix+group)
	}
	return User{Name: v1alpha1.AuthnPrefix + principal.Subject, Groups: groups}
}

func (c *identityCache) prune() {
	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.expires) {
			delete(c.entries, key)
		}
	}
}
