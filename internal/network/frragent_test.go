package network

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func installFRRAgent(t *testing.T, c client.Client, ready int32) {
	t.Helper()
	labels := map[string]string{"app": "frr"}
	agent := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: frrNamespace, Name: frrName},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "frr", Image: "frr"}}}},
		},
	}
	requireAccepted(t, c, agent)
	t.Cleanup(func() { _ = c.Delete(context.Background(), agent) })
	setFRRAgentReady(t, c, ready)
}

func setFRRAgentReady(t *testing.T, c client.Client, ready int32) {
	t.Helper()
	var agent appsv1.DaemonSet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: frrNamespace, Name: frrName}, &agent); err != nil {
		t.Fatal(err)
	}
	agent.Status.NumberReady = ready
	if err := c.Status().Update(context.Background(), &agent); err != nil {
		t.Fatal(err)
	}
}
