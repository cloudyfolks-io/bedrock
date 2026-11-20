package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

func tlsTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestPrepareInitOVNTLS(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	valid, err := network.OVNTLSSecret(now)
	if err != nil {
		t.Fatal(err)
	}
	invalid := valid.DeepCopy()
	invalid.Data["key"] = []byte("invalid key")
	central := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "ovn-central"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "ovn-central", Env: []corev1.EnvVar{{Name: "ENABLE_SSL", Value: "true"}}}},
		}}},
	}
	worker := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "ovs-ovn"},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "openvswitch", Env: []corev1.EnvVar{{Name: "ENABLE_SSL", Value: "false"}}}},
		}}},
	}
	for _, tc := range []struct {
		name, requested, configured, mode, problem string
		objects                                    []client.Object
		key                                        []byte
		when                                       time.Time
	}{
		{name: "default is off", configured: "false", mode: "false"},
		{name: "enable before Fabric", requested: "true", configured: "false", mode: "true"},
		{name: "keep installed TLS on retry", configured: "false", mode: "true", objects: []client.Object{central, valid}, key: valid.Data["key"]},
		{name: "keep installed plain mode with pending upgrade", configured: "true", mode: "false", objects: []client.Object{worker}},
		{name: "reject downgrade", requested: "false", configured: "true", problem: "cannot change OVN TLS", objects: []client.Object{central, valid}, key: valid.Data["key"]},
		{name: "reject upgrade", requested: "true", configured: "false", problem: "cannot change OVN TLS", objects: []client.Object{worker}},
		{name: "reject partial mode change", configured: "false", problem: "disagree", objects: []client.Object{central, worker}},
		{name: "reject invalid key", requested: "true", configured: "false", problem: "invalid certificate or key", objects: []client.Object{invalid}, key: invalid.Data["key"]},
		{name: "reject expired secret", requested: "true", configured: "false", problem: "expired", objects: []client.Object{valid}, key: valid.Data["key"], when: now.AddDate(11, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tlsTestClient(t, tc.objects...)
			when := tc.when
			if when.IsZero() {
				when = now
			}
			mode, err := prepareInitOVNTLS(ctx, c, tc.requested, tc.configured, when)
			if tc.problem != "" {
				if err == nil || !strings.Contains(err.Error(), tc.problem) {
					t.Fatalf("expected error with %q, got %v", tc.problem, err)
				}
			} else if err != nil || mode != tc.mode {
				t.Fatalf("expected mode %q, got %q with error %v", tc.mode, mode, err)
			}
			var secret corev1.Secret
			err = c.Get(ctx, client.ObjectKey{Namespace: network.OVNTLSSecretNamespace, Name: network.OVNTLSSecretName}, &secret)
			if tc.mode != "true" && tc.key == nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected no secret, got error %v", err)
				}
				return
			}
			if err != nil || len(secret.Data["key"]) == 0 {
				t.Fatalf("expected a key in the OVN TLS secret, got error %v", err)
			}
			if tc.key != nil && !bytes.Equal(tc.key, secret.Data["key"]) {
				t.Fatal("the original OVN key changed")
			}
		})
	}
}

func TestRecordInstalledOVNTLSKeepsASettingForAnUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name, start, want string
		present           bool
	}{
		{name: "missing", want: "true"},
		{name: "defaulted", present: true, want: "true"},
		{name: "configured", present: true, start: "false", want: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var objects []client.Object
			if tc.present {
				objects = append(objects, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: initOVNTLSSetting}, Spec: v1alpha1.SettingSpec{Value: tc.start}})
			}
			c := tlsTestClient(t, objects...)
			if err := recordInstalledOVNTLS(ctx, c); err != nil {
				t.Fatal(err)
			}
			var setting v1alpha1.Setting
			if err := c.Get(ctx, client.ObjectKey{Name: initOVNTLSSetting}, &setting); err != nil || setting.Spec.Value != tc.want {
				t.Fatalf("expected setting %q, got %q with error %v", tc.want, setting.Spec.Value, err)
			}
		})
	}
}
