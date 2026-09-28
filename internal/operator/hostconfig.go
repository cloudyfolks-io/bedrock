package operator

import (
	"context"
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var baselineModules = []string{"overlay", "br_netfilter", "kvm", "vhost_net"}

var baselineSysctls = map[string]string{
	"net.ipv4.ip_forward":                 "1",
	"net.bridge.bridge-nf-call-iptables":  "1",
	"net.bridge.bridge-nf-call-ip6tables": "1",
	"fs.inotify.max_user_instances":       "8192",
	"fs.inotify.max_user_watches":         "1048576",
	"vm.max_map_count":                    "262144",
}

func RenderHostConfig(_ v1alpha1.Host, cluster v1alpha1.Cluster) v1alpha1.HostConfigSpec {
	spec := v1alpha1.HostConfigSpec{Modules: slices.Clone(baselineModules), Sysctls: maps.Clone(baselineSysctls)}
	if cluster.Spec.Registry.Mirror != "" {
		spec.ContainerdMirrors = []v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: cluster.Spec.Registry.Mirror}}
	}
	return spec
}

func (r *HostReconciler) ensureHostConfig(ctx context.Context, host v1alpha1.Host, cluster v1alpha1.Cluster) error {
	desired := RenderHostConfig(host, cluster)
	config := &v1alpha1.HostConfig{ObjectMeta: metav1.ObjectMeta{Name: host.Name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, config, func() error {
		config.Labels = map[string]string{v1alpha1.LabelKind: "HostConfig", v1alpha1.LabelName: host.Name}
		config.Spec = desired
		return controllerutil.SetControllerReference(&host, config, r.Client.Scheme())
	})
	return err
}

func (r *HostReconciler) cluster(ctx context.Context) (v1alpha1.Cluster, error) {
	var cluster v1alpha1.Cluster
	err := r.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster)
	if errors.IsNotFound(err) {
		return v1alpha1.Cluster{}, nil
	}
	return cluster, err
}
