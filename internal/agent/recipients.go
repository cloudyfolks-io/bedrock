package agent

import (
	"context"
	"fmt"
	"slices"

	"filippo.io/age"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

const archiveRecipientsKey = "backup.archive-recipients"

func archiveRecipients(ctx context.Context, c client.Client, host age.Recipient, name string, log func(string)) []age.Recipient {
	admins, err := adminRecipients(ctx, c)
	if err != nil {
		log(fmt.Sprintf("%s is invalid, sealing %s to the host key only: %v", archiveRecipientsKey, name, err))
		return []age.Recipient{host}
	}
	return slices.Concat([]age.Recipient{host}, admins)
}

func adminRecipients(ctx context.Context, c client.Client) ([]age.Recipient, error) {
	var setting v1alpha1.Setting
	err := c.Get(ctx, client.ObjectKey{Name: archiveRecipientsKey}, &setting)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return seal.ParseRecipients(setting.Spec.Value)
}
