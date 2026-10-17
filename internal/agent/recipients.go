package agent

import (
	"context"
	"fmt"
	"slices"

	"filippo.io/age"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

const archiveRecipientsKey = "backup.archive-recipients"

func archiveRecipients(ctx context.Context, c client.Client, host age.Recipient, name string, log func(string)) ([]age.Recipient, error) {
	value, err := archiveRecipientsValue(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", archiveRecipientsKey, err)
	}
	admins, err := seal.ParseRecipients(value)
	if err != nil {
		log(fmt.Sprintf("%s is invalid, sealing %s to the host key only: %v", archiveRecipientsKey, name, err))
		return []age.Recipient{host}, nil
	}
	return slices.Concat([]age.Recipient{host}, admins), nil
}

func archiveRecipientsValue(ctx context.Context, c client.Client) (string, error) {
	var setting v1alpha1.Setting
	err := c.Get(ctx, client.ObjectKey{Name: archiveRecipientsKey}, &setting)
	return setting.Spec.Value, client.IgnoreNotFound(err)
}
