package account

import (
	"net/http"
	"slices"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const maxDescription = 256

type tokenBody struct {
	Description string       `json:"description"`
	Scopes      []string     `json:"scopes"`
	ExpiresAt   *metav1.Time `json:"expiresAt"`
}

type tokenView struct {
	ID          string       `json:"id"`
	Description string       `json:"description"`
	Scopes      []string     `json:"scopes"`
	ExpiresAt   *metav1.Time `json:"expiresAt"`
	LastUsed    *metav1.Time `json:"lastUsed"`
}

func NewToken(user string, description string, scopes []string, expiresAt *metav1.Time, token string) v1alpha1.APIToken {
	name := secret.SHA256Hex(token)
	return v1alpha1.APIToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: release.SystemNamespace,
			Labels:    map[string]string{v1alpha1.LabelKind: "APIToken", v1alpha1.LabelName: name[:63]},
		},
		Spec: v1alpha1.APITokenSpec{
			UserRef:     user,
			Scopes:      slices.Clone(scopes),
			ExpiresAt:   copyTime(expiresAt),
			Description: description,
		},
	}
}

func createToken(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	body, err := decodeJSON[tokenBody](r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if utf8.RuneCountInString(body.Description) > maxDescription {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(deps.Clock()) {
		writeError(w, http.StatusBadRequest, "invalid_expiry")
		return
	}
	token, err := secret.NewAPIToken(deps.Random)
	if err != nil {
		internal(w)
		return
	}
	object := NewToken(who.user.Name, body.Description, body.Scopes, body.ExpiresAt, token)
	if err := deps.Client.Create(r.Context(), &object, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		internal(w)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": object.Name, "token": token})
}

func listTokens(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	var tokens v1alpha1.APITokenList
	if err := deps.Client.List(r.Context(), &tokens, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": who.user.Name}); err != nil {
		internal(w)
		return
	}
	writeJSON(w, http.StatusOK, viewTokens(tokens.Items))
}

func revokeToken(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var token v1alpha1.APIToken
	err := deps.Client.Get(r.Context(), clientKey(id), &token)
	switch {
	case apierrors.IsNotFound(err), err == nil && token.Spec.UserRef != who.user.Name:
		writeError(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		internal(w)
		return
	}
	if err := client.IgnoreNotFound(deps.Client.Delete(r.Context(), &token)); err != nil {
		internal(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func viewTokens(tokens []v1alpha1.APIToken) []tokenView {
	views := make([]tokenView, 0, len(tokens))
	for _, token := range tokens {
		views = append(views, tokenView{
			ID:          token.Name,
			Description: token.Spec.Description,
			Scopes:      append([]string{}, token.Spec.Scopes...),
			ExpiresAt:   token.Spec.ExpiresAt,
			LastUsed:    token.Status.LastUsed,
		})
	}
	return views
}

func copyTime(value *metav1.Time) *metav1.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
