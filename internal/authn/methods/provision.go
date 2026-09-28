package methods

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type ExternalIdentity struct {
	Username    string
	DisplayName string
	Email       string
	Subject     string
	Groups      []string
}

func ProvisionUser(existing *v1alpha1.User, provider string, identity ExternalIdentity) v1alpha1.User {
	name := v1alpha1.UserObjectName(identity.Username)
	user := v1alpha1.User{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "User"},
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name}},
	}
	if existing != nil {
		user = *existing
	}
	user.Spec.Username = identity.Username
	user.Spec.DisplayName = identity.DisplayName
	user.Spec.Email = identity.Email
	user.Spec.Source = provider
	user.Spec.Groups = identity.Groups
	if identity.Subject != "" {
		user.Status.UpstreamSubject = identity.Subject
	}
	return user
}

var errUserConflict = errors.New("methods: user is bound to a different identity")

func sourceMismatch(existing v1alpha1.User, providerName string) bool {
	return existing.Spec.Source != providerName
}

func subjectMismatch(existing v1alpha1.User, subject string) bool {
	return existing.Status.UpstreamSubject != "" && existing.Status.UpstreamSubject != subject
}

func CreateOrExistingUser(ctx context.Context, c client.Client, created v1alpha1.User) (v1alpha1.User, bool, error) {
	err := c.Create(ctx, &created, client.FieldOwner(v1alpha1.AuthnFieldManager))
	if err == nil {
		return created, false, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return v1alpha1.User{}, false, err
	}
	var fresh v1alpha1.User
	if err := c.Get(ctx, client.ObjectKey{Namespace: created.Namespace, Name: created.Name}, &fresh); err != nil {
		return v1alpha1.User{}, false, err
	}
	return fresh, true, nil
}
