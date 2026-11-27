package cli

import (
	"context"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const initOVNTLSSetting = "network.ovn-tls"

func recordInstalledOVNTLS(ctx context.Context, c client.Client) error {
	desired := &v1alpha1.Setting{
		ObjectMeta: metav1.ObjectMeta{
			Name:   initOVNTLSSetting,
			Labels: map[string]string{v1alpha1.LabelKind: "Setting", v1alpha1.LabelName: initOVNTLSSetting},
		},
		Spec: v1alpha1.SettingSpec{Value: "true"},
	}
	return createOrUpdateSpec(ctx, c, desired, func(current *v1alpha1.Setting) {
		if current.Spec.Value == "" {
			current.Spec = desired.Spec
		}
	})
}

func prepareInitOVNTLS(ctx context.Context, c client.Client, requested, configured string, now time.Time) (string, error) {
	installed, err := installedOVNTLSMode(ctx, c)
	if err != nil {
		return "", err
	}
	if installed != "" && requested != "" && requested != installed {
		return "", fmt.Errorf("init cannot change OVN TLS from %s to %s on an existing Fabric installation. Use the network.ovn-tls setting and the upgrade procedure", installed, requested)
	}
	mode := requested
	if installed != "" {
		mode = installed
	}
	if mode == "" {
		mode = configured
	}
	if mode != "true" && mode != "false" {
		return "", fmt.Errorf("network.ovn-tls must be true or false")
	}
	if mode == "true" {
		if err := network.EnsureOVNTLSSecret(ctx, c, now); err != nil {
			return "", fmt.Errorf("prepare OVN TLS before installing Fabric: %w", err)
		}
	}
	return mode, nil
}

func installedOVNTLSMode(ctx context.Context, c client.Reader) (string, error) {
	var central appsv1.Deployment
	var ovs appsv1.DaemonSet
	workloads := []struct {
		name       string
		container  string
		object     client.Object
		containers *[]corev1.Container
	}{
		{"ovn-central", "ovn-central", &central, &central.Spec.Template.Spec.Containers},
		{"ovs-ovn", "openvswitch", &ovs, &ovs.Spec.Template.Spec.Containers},
	}
	var installed string
	for _, workload := range workloads {
		key := client.ObjectKey{Namespace: network.OVNTLSSecretNamespace, Name: workload.name}
		if err := c.Get(ctx, key, workload.object); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("read the OVN TLS mode of %s: %w", key, err)
		}
		containers := *workload.containers
		index := slices.IndexFunc(containers, func(container corev1.Container) bool { return container.Name == workload.container })
		if index < 0 {
			return "", fmt.Errorf("cannot determine the OVN TLS mode because %s has no %s container", key, workload.container)
		}
		env := containers[index].Env
		index = slices.IndexFunc(env, func(value corev1.EnvVar) bool { return value.Name == "ENABLE_SSL" })
		if index < 0 || (env[index].Value != "true" && env[index].Value != "false") {
			return "", fmt.Errorf("cannot determine the OVN TLS mode of %s", key)
		}
		mode := env[index].Value
		if installed != "" && installed != mode {
			return "", fmt.Errorf("Fabric workloads disagree on the OVN TLS mode. Complete the Fabric rollout before retrying init")
		}
		installed = mode
	}
	return installed, nil
}
