package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/audit"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func syncAuditFiles(ctx context.Context, c client.Client, deps Deps, current v1alpha1.Host) error {
	if !v1alpha1.HostHasRole(current, v1alpha1.RoleControlPlane) {
		return nil
	}
	policy, err := auditPolicySource(ctx, c)
	if err != nil {
		return err
	}
	if err := audit.Validate(policy); err != nil {
		return fmt.Errorf("invalid policy in %s: %w", audit.ConfigMapName, err)
	}
	if _, err := apiserver.WriteFiles(filepath.Join(deps.Root, audit.LogDir), nil, deps.AuthnOwner); err != nil {
		return err
	}
	_, err = apiserver.WriteFiles(filepath.Join(deps.Root, audit.Dir), map[string][]byte{audit.PolicyFile: policy}, deps.AuthnOwner)
	return err
}

func auditPolicySource(ctx context.Context, c client.Client) ([]byte, error) {
	var config corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: audit.ConfigMapName}, &config); err != nil {
		return nil, sourceError(audit.ConfigMapName, err)
	}
	policy := config.Data[audit.PolicyFile]
	if policy == "" {
		return nil, waitingFor(audit.ConfigMapName)
	}
	return []byte(policy), nil
}

func withAuditMessage(status *v1alpha1.AuthnFilesStatus, auditErr error) *v1alpha1.AuthnFilesStatus {
	if auditErr == nil || status == nil {
		return status
	}
	next := status.DeepCopy()
	messages := []string{"audit: " + auditErr.Error()}
	if next.Message != "" {
		messages = []string{next.Message, messages[0]}
	}
	next.Message = strings.Join(messages, "; ")
	return next
}
