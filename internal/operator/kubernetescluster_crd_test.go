package operator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func validWorker(name string) v1alpha1.KubernetesWorker {
	return v1alpha1.KubernetesWorker{
		Name:           name,
		Image:          v1alpha1.ImageRef{Name: "ubuntu-24.04"},
		SecurityGroups: []string{"k8s"},
	}
}

func validKubernetesCluster(name string) *v1alpha1.KubernetesCluster {
	return &v1alpha1.KubernetesCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.KubernetesClusterSpec{
			Version:      "v1.36.3+k0s.1",
			ControlPlane: v1alpha1.KubernetesControlPlane{SecurityGroups: []string{"k8s"}},
			Workers:      []v1alpha1.KubernetesWorker{validWorker("a"), validWorker("b")},
		},
	}
}

func getKubernetesCluster(t *testing.T, c client.Client, name string) *v1alpha1.KubernetesCluster {
	t.Helper()
	var fresh v1alpha1.KubernetesCluster
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &fresh); err != nil {
		t.Fatal(err)
	}
	return &fresh
}

func TestKubernetesClusterDefaults(t *testing.T) {
	c, _ := StartTestEnv(t)
	createValid(t, c, validKubernetesCluster("defaults"))
	fresh := getKubernetesCluster(t, c, "defaults")
	got := map[string]any{
		"subnetRef":   fresh.Spec.Network.SubnetRef,
		"podCIDR":     fresh.Spec.Network.PodCIDR,
		"serviceCIDR": fresh.Spec.Network.ServiceCIDR,
		"cores":       fresh.Spec.Workers[0].Compute.Cores,
		"memoryMiB":   fresh.Spec.Workers[0].Compute.MemoryMiB,
		"sizeGi":      fresh.Spec.Workers[0].RootDisk.SizeGi,
		"imageScope":  fresh.Spec.Workers[0].Image.Scope,
		"suspended":   fresh.Spec.Suspended,
	}
	want := map[string]any{
		"subnetRef": "default", "podCIDR": "100.64.0.0/16", "serviceCIDR": "100.65.0.0/16",
		"cores": int32(2), "memoryMiB": int32(4096), "sizeGi": int32(40), "imageScope": "cluster", "suspended": false,
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
}

func TestKubernetesClusterStatusSubresource(t *testing.T) {
	c, _ := StartTestEnv(t)
	cluster := validKubernetesCluster("status")
	createValid(t, c, cluster)
	cluster.Status.Phase = v1alpha1.KubernetesClusterPhaseReady
	cluster.Status.ControlPlane = v1alpha1.KubernetesControlPlaneStatus{Address: "10.0.0.10", Ready: true}
	cluster.Status.Endpoint = "https://10.0.0.10:6443"
	cluster.Status.KubeconfigSecret = "status-kubeconfig"
	cluster.Status.Workers = []v1alpha1.KubernetesWorkerStatus{{Name: "a", Phase: "Running", VM: "status-a"}}
	if err := c.Status().Update(context.Background(), cluster); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	cluster.Status.Phase = "Paused"
	requireInvalid(t, c.Status().Update(context.Background(), cluster), "Unsupported value")
}

func TestKubernetesClusterRefusesWrongValues(t *testing.T) {
	c, _ := StartTestEnv(t)
	longName := strings.Repeat("a", 41)
	cases := []struct {
		name   string
		mutate func(*v1alpha1.KubernetesCluster)
		want   string
	}{
		{"version-without-suffix", func(x *v1alpha1.KubernetesCluster) { x.Spec.Version = "v1.36.3" }, "spec.version"},
		{"version-with-dash", func(x *v1alpha1.KubernetesCluster) { x.Spec.Version = "v1.36.3-k0s.1" }, "spec.version"},
		{"version-without-v", func(x *v1alpha1.KubernetesCluster) { x.Spec.Version = "1.36.3+k0s.1" }, "spec.version"},
		{"no-control-plane-groups", func(x *v1alpha1.KubernetesCluster) { x.Spec.ControlPlane.SecurityGroups = nil }, "securityGroups"},
		{"empty-control-plane-groups", func(x *v1alpha1.KubernetesCluster) { x.Spec.ControlPlane.SecurityGroups = []string{} }, "securityGroups"},
		{"control-plane-address-not-ip", func(x *v1alpha1.KubernetesCluster) { x.Spec.ControlPlane.Address = "host" }, "address must be an IPv4 address"},
		{"control-plane-address-ipv6", func(x *v1alpha1.KubernetesCluster) { x.Spec.ControlPlane.Address = "fd00::1" }, "address must be an IPv4 address"},
		{"pod-cidr", func(x *v1alpha1.KubernetesCluster) { x.Spec.Network.PodCIDR = "100.64.0.0" }, "podCIDR must be an IPv4 CIDR"},
		{"service-cidr-ipv6", func(x *v1alpha1.KubernetesCluster) { x.Spec.Network.ServiceCIDR = "fd00::/64" }, "serviceCIDR must be an IPv4 CIDR"},
		{"worker-no-groups", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].SecurityGroups = nil }, "securityGroups"},
		{"worker-name-upper", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Name = "Big" }, "spec.workers[0].name"},
		{"worker-cores-low", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Compute.Cores = -1 }, "cores"},
		{"worker-cores-high", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Compute.Cores = 17 }, "cores"},
		{"worker-memory-low", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Compute.MemoryMiB = 1023 }, "memoryMiB"},
		{"worker-memory-high", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Compute.MemoryMiB = 65537 }, "memoryMiB"},
		{"worker-disk-low", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].RootDisk.SizeGi = -5 }, "sizeGi"},
		{"worker-disk-high", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].RootDisk.SizeGi = 501 }, "sizeGi"},
		{"worker-address", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Address = "nope" }, "address must be an IPv4 address"},
		{"worker-ssh-keys", func(x *v1alpha1.KubernetesCluster) {
			for i := 0; i < 11; i++ {
				x.Spec.Workers[0].SSHKeys = append(x.Spec.Workers[0].SSHKeys, "key"+strings.Repeat("a", i+1))
			}
		}, "sshKeys"},
		{"workers-above-fifty", func(x *v1alpha1.KubernetesCluster) {
			x.Spec.Workers = nil
			for i := 0; i < 51; i++ {
				x.Spec.Workers = append(x.Spec.Workers, validWorker(fmt.Sprintf("w%d", i)))
			}
		}, "Too many"},
		{"worker-name-twice", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers = append(x.Spec.Workers, validWorker("a")) }, "Duplicate value"},
		{"worker-image-name-empty", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Image.Name = "" }, "image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := validKubernetesCluster("wrong-" + tc.name)
			tc.mutate(x)
			createInvalid(t, c, x, tc.want)
		})
	}
	t.Run("name-too-long", func(t *testing.T) {
		createInvalid(t, c, validKubernetesCluster(longName), "the name must have at most 40 characters")
	})
	t.Run("name-not-a-dns-label", func(t *testing.T) {
		createInvalid(t, c, validKubernetesCluster("a.b"), "the name must be a DNS label")
	})
}

func TestKubernetesClusterNameAndWorkerNameFitTheVMName(t *testing.T) {
	c, _ := StartTestEnv(t)
	cluster := validKubernetesCluster(strings.Repeat("c", 40))
	cluster.Spec.Workers = []v1alpha1.KubernetesWorker{validWorker(strings.Repeat("w", 11))}
	createValid(t, c, cluster)
	tooLong := validKubernetesCluster(strings.Repeat("d", 40))
	tooLong.Spec.Workers = []v1alpha1.KubernetesWorker{validWorker(strings.Repeat("w", 12))}
	createInvalid(t, c, tooLong, "the cluster name, a dash and a worker name must have at most 52 characters")
	shortName := validKubernetesCluster("short")
	shortName.Spec.Workers = []v1alpha1.KubernetesWorker{validWorker(strings.Repeat("w", 46)), validWorker("ok")}
	createValid(t, c, shortName)
	shortTooLong := validKubernetesCluster("shorter")
	shortTooLong.Spec.Workers = []v1alpha1.KubernetesWorker{validWorker("ok"), validWorker(strings.Repeat("w", 45))}
	createInvalid(t, c, shortTooLong, "the cluster name, a dash and a worker name must have at most 52 characters")
}

func TestKubernetesClusterAcceptsTheLimits(t *testing.T) {
	c, _ := StartTestEnv(t)
	cluster := validKubernetesCluster("limits")
	cluster.Spec.Workers = nil
	for i := 0; i < 50; i++ {
		worker := validWorker(fmt.Sprintf("w%d", i))
		worker.Compute = v1alpha1.KubernetesWorkerCompute{Cores: 16, MemoryMiB: 65536}
		worker.RootDisk.SizeGi = 500
		cluster.Spec.Workers = append(cluster.Spec.Workers, worker)
	}
	cluster.Spec.Workers[0].Compute = v1alpha1.KubernetesWorkerCompute{Cores: 1, MemoryMiB: 1024}
	cluster.Spec.Workers[0].RootDisk.SizeGi = 20
	for i := 0; i < 10; i++ {
		cluster.Spec.Workers[0].SSHKeys = append(cluster.Spec.Workers[0].SSHKeys, "key"+strings.Repeat("a", i+1))
	}
	createValid(t, c, cluster)
}

func TestKubernetesClusterImmutableFields(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	cluster := validKubernetesCluster("immutable")
	cluster.Spec.ControlPlane.Address = "10.0.0.10"
	cluster.Spec.Workers[0].Address = "10.0.0.20"
	createValid(t, c, cluster)

	cases := []struct {
		name   string
		mutate func(*v1alpha1.KubernetesCluster)
		want   string
	}{
		{"version", func(x *v1alpha1.KubernetesCluster) { x.Spec.Version = "v1.36.4+k0s.0" }, "version is immutable"},
		{"subnet", func(x *v1alpha1.KubernetesCluster) { x.Spec.Network.SubnetRef = "other" }, "network is immutable"},
		{"pod-cidr", func(x *v1alpha1.KubernetesCluster) { x.Spec.Network.PodCIDR = "100.66.0.0/16" }, "network is immutable"},
		{"service-cidr", func(x *v1alpha1.KubernetesCluster) { x.Spec.Network.ServiceCIDR = "100.67.0.0/16" }, "network is immutable"},
		{"control-plane-address", func(x *v1alpha1.KubernetesCluster) { x.Spec.ControlPlane.Address = "10.0.0.11" }, "address is immutable"},
		{"control-plane-address-removed", func(x *v1alpha1.KubernetesCluster) { x.Spec.ControlPlane.Address = "" }, "address is immutable"},
		{"worker-address", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Address = "10.0.0.21" }, "a worker address is immutable"},
		{"worker-address-removed", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[0].Address = "" }, "a worker address is immutable"},
		{"worker-address-added", func(x *v1alpha1.KubernetesCluster) { x.Spec.Workers[1].Address = "10.0.0.22" }, "a worker address is immutable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fresh := getKubernetesCluster(t, c, "immutable")
			tc.mutate(fresh)
			updateInvalid(t, c, fresh, tc.want)
		})
	}

	fresh := getKubernetesCluster(t, c, "immutable")
	fresh.Spec.Suspended = true
	fresh.Spec.ControlPlane.SecurityGroups = []string{"k8s", "extra"}
	fresh.Spec.Workers[0].Compute.Cores = 8
	fresh.Spec.Workers[1].SecurityGroups = []string{"k8s", "extra"}
	if err := c.Update(ctx, fresh); err != nil {
		t.Fatalf("suspend, groups and worker size must stay editable: %v", err)
	}
}

func TestKubernetesClusterWithoutWorkersCanGainAndLoseThem(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	cluster := validKubernetesCluster("no-workers")
	cluster.Spec.Workers = nil
	createValid(t, c, cluster)
	fresh := getKubernetesCluster(t, c, "no-workers")
	fresh.Spec.Workers = []v1alpha1.KubernetesWorker{validWorker("a")}
	if err := c.Update(ctx, fresh); err != nil {
		t.Fatalf("a cluster without workers must accept a worker: %v", err)
	}
	fresh = getKubernetesCluster(t, c, "no-workers")
	fresh.Spec.Workers = nil
	if err := c.Update(ctx, fresh); err != nil {
		t.Fatalf("a cluster must accept the removal of every worker: %v", err)
	}
}

func TestKubernetesClusterWorkersAreAMap(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	cluster := validKubernetesCluster("workers-map")
	createValid(t, c, cluster)
	fresh := getKubernetesCluster(t, c, "workers-map")
	fresh.Spec.Workers = append(fresh.Spec.Workers, validWorker("c"))
	if err := c.Update(ctx, fresh); err != nil {
		t.Fatalf("add a worker: %v", err)
	}
	fresh = getKubernetesCluster(t, c, "workers-map")
	fresh.Spec.Workers = fresh.Spec.Workers[1:]
	if err := c.Update(ctx, fresh); err != nil {
		t.Fatalf("remove a worker: %v", err)
	}
	fresh = getKubernetesCluster(t, c, "workers-map")
	if len(fresh.Spec.Workers) != 2 || fresh.Spec.Workers[0].Name != "b" || fresh.Spec.Workers[1].Name != "c" {
		t.Fatalf("workers %+v", fresh.Spec.Workers)
	}

	other := getKubernetesCluster(t, c, "workers-map")
	other.Spec.Workers[0].Compute.Cores = 4
	patch := client.MergeFrom(getKubernetesCluster(t, c, "workers-map"))
	if err := c.Patch(ctx, other, patch); err != nil {
		t.Fatal(err)
	}
	apply := getKubernetesCluster(t, c, "workers-map")
	if apply.Spec.Workers[0].Compute.Cores != 4 || apply.Spec.Workers[1].Compute.Cores != 2 {
		t.Fatalf("workers %+v", apply.Spec.Workers)
	}
}
