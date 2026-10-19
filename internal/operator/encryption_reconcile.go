package operator

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

const rewritePageSize = 200

func (r *ClusterReconciler) reconcileEncryption(ctx context.Context) (time.Duration, error) {
	var cluster v1alpha1.Cluster
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return 0, client.IgnoreNotFound(err)
	}
	status := rotationStatusOf(cluster)
	action := cluster.Spec.Encryption.Action
	if action == "" && status.Phase == "" {
		return 0, nil
	}
	secret, err := readEncryptionContent(ctx, r.APIReader)
	if err != nil {
		return 0, err
	}
	var hosts v1alpha1.HostList
	if err := r.APIReader.List(ctx, &hosts); err != nil {
		return 0, err
	}
	decision := nextRotation(rotationInput{
		Action:    action,
		KMS:       cluster.Spec.Encryption.KMS != nil,
		Upgrading: cluster.Status.Upgrade != nil,
		Status:    status,
		Secret:    secret,
		Hosts:     hosts.Items,
		Now:       time.Now(),
		Random:    rand.Reader,
	})
	if decision.Rewrite {
		decision, err = r.rewriteAndRecord(ctx, decision)
		if err != nil {
			return 0, err
		}
	}
	if err := r.applyRotation(ctx, cluster, decision); err != nil {
		return 0, err
	}
	if decision.Status.Phase == "" {
		return 0, nil
	}
	return r.Interval, nil
}

func (r *ClusterReconciler) rewriteAndRecord(ctx context.Context, decision rotationDecision) (rotationDecision, error) {
	rewritten, err := rewriteSecrets(ctx, r.APIReader, r.Client, rewritePageSize)
	if err != nil {
		failed := decision.Status
		failed.Message = fmt.Sprintf("rewriting the Secrets failed, it runs again: %v", err)
		return decision, errors.Join(err, r.writeEncryptionStatus(ctx, failed))
	}
	decision.Status = finishRewrite(decision.Status, rewritten)
	decision.Rewrite = false
	return decision, nil
}

func rotationStatusOf(cluster v1alpha1.Cluster) v1alpha1.EncryptionRotationStatus {
	if cluster.Status.Encryption == nil {
		return v1alpha1.EncryptionRotationStatus{}
	}
	return *cluster.Status.Encryption.DeepCopy()
}

func readEncryptionContent(ctx context.Context, reader client.Reader) ([]byte, error) {
	var secret corev1.Secret
	err := reader.Get(ctx, client.ObjectKey{Namespace: encryption.SecretNamespace, Name: encryption.SecretName}, &secret)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return secret.Data[encryption.SecretKey], nil
}

func (r *ClusterReconciler) applyRotation(ctx context.Context, cluster v1alpha1.Cluster, decision rotationDecision) error {
	if err := r.writeEncryptionStatus(ctx, decision.Status); err != nil {
		return err
	}
	if decision.Secret != nil {
		if err := writeEncryptionSecret(ctx, r.Client, decision.Secret); err != nil {
			return err
		}
	}
	for _, name := range decision.Revoke {
		if err := patchHostAnnotation(ctx, r.Client, name, nil); err != nil {
			return err
		}
	}
	if decision.Grant != "" {
		hash := decision.GrantHash
		if err := patchHostAnnotation(ctx, r.Client, decision.Grant, &hash); err != nil {
			return err
		}
	}
	if decision.ClearAction {
		return clearEncryptionAction(ctx, r.Client, cluster.Spec.Encryption.Action)
	}
	return nil
}

func (r *ClusterReconciler) writeEncryptionStatus(ctx context.Context, status v1alpha1.EncryptionRotationStatus) error {
	return writeClusterStatus(ctx, r.Client, func(s *v1alpha1.ClusterStatus) {
		if equality.Semantic.DeepEqual(status, v1alpha1.EncryptionRotationStatus{}) {
			s.Encryption = nil
			return
		}
		s.Encryption = &status
	})
}

func writeEncryptionSecret(ctx context.Context, c client.Client, content []byte) error {
	body, err := json.Marshal(map[string]any{"data": map[string][]byte{encryption.SecretKey: content}})
	if err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: encryption.SecretNamespace, Name: encryption.SecretName}}
	return c.Patch(ctx, secret, client.RawPatch(types.MergePatchType, body), client.FieldOwner(v1alpha1.OperatorFieldManager))
}

func patchHostAnnotation(ctx context.Context, c client.Client, name string, value *string) error {
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]*string{v1alpha1.AnnotationEncryptionApply: value}}})
	if err != nil {
		return err
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return client.IgnoreNotFound(c.Patch(ctx, host, client.RawPatch(types.MergePatchType, body), client.FieldOwner(v1alpha1.OperatorFieldManager)))
}

func clearEncryptionAction(ctx context.Context, c client.Client, action string) error {
	return retry.OnError(statusWriteBackoff, apierrors.IsConflict, func() error {
		var current v1alpha1.Cluster
		if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if current.Spec.Encryption.Action != action {
			return nil
		}
		cleared := current.DeepCopy()
		cleared.Spec.Encryption.Action = ""
		return c.Patch(ctx, cleared, client.MergeFromWithOptions(&current, client.MergeFromWithOptimisticLock{}))
	})
}

func rewriteSecrets(ctx context.Context, reader client.Reader, writer client.Client, pageSize int64) (int64, error) {
	var rewritten int64
	token := ""
	for {
		var page corev1.SecretList
		if err := reader.List(ctx, &page, client.Limit(pageSize), client.Continue(token)); err != nil {
			return rewritten, err
		}
		for index := range page.Items {
			err := writer.Update(ctx, &page.Items[index])
			if err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
				return rewritten, fmt.Errorf("rewrite secret %s/%s: %w", page.Items[index].Namespace, page.Items[index].Name, err)
			}
			rewritten++
		}
		token = page.Continue
		if token == "" {
			return rewritten, nil
		}
	}
}
