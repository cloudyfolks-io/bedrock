package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	touchInterval = time.Minute
	maxBody       = 1 << 20
)

func Handler(reader client.Reader, writer client.Client, clock func() time.Time, bearer func(context.Context) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		want, err := bearer(r.Context())
		if err != nil || want == "" {
			http.Error(w, "webhook bearer unavailable", http.StatusServiceUnavailable)
			return
		}
		if !authorized(r.Header.Get("Authorization"), want) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request authenticationv1.TokenReview
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&request); err != nil {
			http.Error(w, "invalid token review", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authenticationv1.TokenReview{
			TypeMeta: metav1.TypeMeta{APIVersion: authenticationv1.SchemeGroupVersion.String(), Kind: "TokenReview"},
			Status:   lookup(r.Context(), reader, writer, request.Spec.Token, clock()),
		})
	})
}

func Review(token v1alpha1.APIToken, user v1alpha1.User, groups []string, now time.Time) authenticationv1.TokenReviewStatus {
	if user.Name == "" || token.Spec.UserRef != user.Name || user.Spec.Disabled || expired(token.Spec.ExpiresAt, now) {
		return authenticationv1.TokenReviewStatus{}
	}
	return authenticationv1.TokenReviewStatus{
		Authenticated: true,
		User: authenticationv1.UserInfo{
			Username: v1alpha1.AuthnPrefix + user.Name,
			UID:      string(user.UID),
			Groups:   prefixed(groups),
		},
	}
}

func TouchDue(lastUsed *metav1.Time, now time.Time) bool {
	return lastUsed == nil || now.Sub(lastUsed.Time) >= touchInterval
}

func lookup(ctx context.Context, reader client.Reader, writer client.Client, token string, now time.Time) authenticationv1.TokenReviewStatus {
	if !secret.IsAPIToken(token) {
		return authenticationv1.TokenReviewStatus{}
	}
	var stored v1alpha1.APIToken
	if err := reader.Get(ctx, objectKey(secret.SHA256Hex(token)), &stored); err != nil {
		return authenticationv1.TokenReviewStatus{}
	}
	var user v1alpha1.User
	if err := reader.Get(ctx, objectKey(stored.Spec.UserRef), &user); err != nil {
		return authenticationv1.TokenReviewStatus{}
	}
	var groups v1alpha1.GroupList
	if err := reader.List(ctx, &groups, client.InNamespace(release.SystemNamespace)); err != nil {
		return authenticationv1.TokenReviewStatus{}
	}
	status := Review(stored, user, policy.EffectiveGroups(user, groups.Items), now)
	if status.Authenticated && TouchDue(stored.Status.LastUsed, now) {
		_ = touch(ctx, writer, stored.Name, now)
	}
	return status
}

func touch(ctx context.Context, writer client.Client, name string, now time.Time) error {
	used := metav1.NewTime(now)
	return ssa.ApplyStatus(ctx, writer, &v1alpha1.APIToken{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "APIToken"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace},
		Status:     v1alpha1.APITokenStatus{LastUsed: &used},
	}, v1alpha1.AuthnFieldManager)
}

func authorized(header, want string) bool {
	given, found := strings.CutPrefix(header, "Bearer ")
	return found && given != "" && secret.Equal(given, want)
}

func expired(expiresAt *metav1.Time, now time.Time) bool {
	return expiresAt != nil && !now.Before(expiresAt.Time)
}

func prefixed(groups []string) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, v1alpha1.AuthnPrefix+group)
	}
	slices.Sort(out)
	return out
}

func objectKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: release.SystemNamespace, Name: name}
}
