package images

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const testPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func setAdditionalCA(t *testing.T, c client.Client, value string) {
	t.Helper()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "platform.additional-ca"}}
	err := c.Get(context.Background(), client.ObjectKeyFromObject(setting), setting)
	if apierrors.IsNotFound(err) {
		setting.Spec.Value = value
		err = c.Create(context.Background(), setting)
	} else if err == nil {
		setting.Spec.Value = value
		err = c.Update(context.Background(), setting)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func additionalCAIn(c client.Client, namespace string) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "platform-additional-ca"}, &cm)
	return &cm, err
}

func TestAdditionalCAConfigMapFollowsTheSetting(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	s := imageSubject(httpSpec("https://example.com/rocky.qcow2"), "rocky")
	l := &loop{t: t, c: c}

	setAdditionalCA(t, c, testPEM)
	l.pass(s)
	cm, err := additionalCAIn(c, "acme-web")
	if err != nil || cm.Data["ca.crt"] != testPEM {
		t.Fatalf("config map %v %v", cm, err)
	}
	dv := fetch(t, c, DataVolumeGVK, "acme-web", "rocky-import")
	if got := nested(t, dv, "spec", "source", "http", "certConfigMap"); got != "platform-additional-ca" {
		t.Fatalf("certConfigMap %v", got)
	}

	setAdditionalCA(t, c, "")
	if err := c.Delete(context.Background(), dv); err != nil {
		t.Fatal(err)
	}
	l.pass(s)
	if _, err := additionalCAIn(c, "acme-web"); !apierrors.IsNotFound(err) {
		t.Fatalf("config map must be gone: %v", err)
	}
	dv = fetch(t, c, DataVolumeGVK, "acme-web", "rocky-import")
	if got, found := dv.Object["spec"].(map[string]any)["source"].(map[string]any)["http"].(map[string]any)["certConfigMap"]; found {
		t.Fatalf("certConfigMap %v", got)
	}
}

func TestAdditionalCALeavesForeignConfigMaps(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "platform-additional-ca", Namespace: "acme-web"}, Data: map[string]string{"ca.crt": "mine"}}
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAdditionalCA(context.Background(), c, "acme-web", ""); err != nil {
		t.Fatal(err)
	}
	if cm, err := additionalCAIn(c, "acme-web"); err != nil || cm.Data["ca.crt"] != "mine" {
		t.Fatalf("config map %v %v", cm, err)
	}
}

func TestAdditionalCAIsPerNamespace(t *testing.T) {
	c := startEnv(t)
	tenantNamespace(t, c, "acme-web")
	ensureNamespace(t, c, "acme-db", map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "db"})
	setAdditionalCA(t, c, testPEM)

	for _, namespace := range []string{"acme-web", "acme-db"} {
		img := ubuntuImage()
		img.Namespace = namespace
		img.Spec = httpSpec("https://example.com/a.qcow2")
		s := SubjectOfImage(img, "acme", "web", false)
		if _, err := reconcileSubject(context.Background(), c, s, v1alpha1.ImageStatus{}, testNow); err != nil {
			t.Fatal(err)
		}
		if cm, err := additionalCAIn(c, namespace); err != nil || cm.Data["ca.crt"] != testPEM {
			t.Fatalf("%s: %v %v", namespace, cm, err)
		}
	}
}

func TestAdditionalCAPEMReadsTheSetting(t *testing.T) {
	c := startEnv(t)
	if got, err := AdditionalCAPEM(context.Background(), c); err != nil || got != "" {
		t.Fatalf("empty: %q %v", got, err)
	}
	setAdditionalCA(t, c, testPEM)
	if got, err := AdditionalCAPEM(context.Background(), c); err != nil || got != testPEM {
		t.Fatalf("set: %q %v", got, err)
	}
}
