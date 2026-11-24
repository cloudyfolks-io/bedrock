package saxess

import (
	"context"
	"slices"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateBedrockInvite is a trusted administrator operation, not a public API.
// The UID prevents a recreated Bedrock user from inheriting card enrollment.
func (b *Backend) CreateBedrockInvite(ctx context.Context, reader client.Reader, key client.ObjectKey, ttl time.Duration) (EnrollmentInvite, error) {
	var user v1alpha1.User
	if err := reader.Get(ctx, key, &user); err != nil {
		return EnrollmentInvite{}, err
	}
	if user.UID == "" || user.Spec.Disabled || !user.DeletionTimestamp.IsZero() {
		return EnrollmentInvite{}, ErrEnrollmentRejected
	}
	var groups v1alpha1.GroupList
	if err := reader.List(ctx, &groups, client.InNamespace(key.Namespace)); err != nil {
		return EnrollmentInvite{}, err
	}
	if !slices.Contains(policy.EffectiveGroups(user, groups.Items), v1alpha1.GroupAdmins) {
		return EnrollmentInvite{}, ErrEnrollmentRejected
	}
	return b.CreateInvite(ctx, string(user.UID), user.Spec.Email, ttl)
}
