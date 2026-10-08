package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const clusterEventNamespace = "default"

func warn(ctx context.Context, c client.Client, regarding corev1.ObjectReference, reason, message string) error {
	namespace := regarding.Namespace
	if namespace == "" {
		namespace = clusterEventNamespace
	}
	sum := sha256.Sum256([]byte(reason + "/" + message))
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: namespace, Name: regarding.Name + "." + hex.EncodeToString(sum[:6])},
		InvolvedObject: regarding,
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        message,
		Source:         corev1.EventSource{Component: v1alpha1.NetworkFieldManager},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, event))
}

func settingReference(key string) corev1.ObjectReference {
	return corev1.ObjectReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Setting", Name: key}
}
