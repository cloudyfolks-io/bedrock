package methods

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
