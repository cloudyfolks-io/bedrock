package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestOVNTLSSecretHoldsACertificateTheCAIssued(t *testing.T) {
	secret, err := OVNTLSSecret(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(secret.Data["cert"], secret.Data["key"])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(secret.Data["cacert"]) {
		t.Fatal("the ca is not a certificate")
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatal(err)
	}
	if secret.Name != "fabric-tls" || secret.Namespace != "kube-system" {
		t.Fatalf("secret %s/%s", secret.Namespace, secret.Name)
	}
}

func TestUnderlayReconcilerMakesTheOVNTLSSecretOnceWhenEnabled(t *testing.T) {
	c, _ := testEnv(t)
	cleanUnderlay(t, c)
	ctx := context.Background()
	t.Cleanup(func() {
		_ = c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "fabric-tls"}})
	})
	reconcileUnderlay(t, c)
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "fabric-tls"}, &secret); !apierrors.IsNotFound(err) {
		t.Fatalf("no secret while the setting is off: %v", err)
	}
	setSetting(t, "network.ovn-tls", "true")
	reconcileUnderlay(t, c)
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "fabric-tls"}, &secret); err != nil {
		t.Fatal(err)
	}
	first := string(secret.Data["key"])
	reconcileUnderlay(t, c)
	if err := c.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "fabric-tls"}, &secret); err != nil || string(secret.Data["key"]) != first {
		t.Fatalf("the secret must stay as it is: %v", err)
	}
}
