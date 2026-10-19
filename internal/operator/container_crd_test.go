package operator

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func validContainer(name string) *v1alpha1.Container {
	return &v1alpha1.Container{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.ContainerSpec{
			Image:   "registry.test/web:1",
			Command: []string{"/bin/web"},
			Args:    []string{"--port", "8080"},
			Env: []v1alpha1.ContainerEnvVar{
				{Name: "MODE", Value: "prod"},
				{Name: "TOKEN", ValueFrom: &v1alpha1.ContainerEnvSource{SecretKeyRef: &v1alpha1.ContainerSecretKeyRef{Name: "creds", Key: "token"}}},
			},
			Ports:   []v1alpha1.ContainerPort{{Name: "http", Port: 8080, Protocol: "TCP"}},
			FSGroup: ptr.To(int64(1000)),
			Probes: &v1alpha1.ContainerProbes{
				Liveness:  &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"true"}}},
				Readiness: &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"true"}}},
			},
			ImagePullSecrets: []string{"registry-credentials"},
			Network:          v1alpha1.ContainerNetwork{SubnetRef: "default", SecurityGroups: []string{"web"}},
		},
	}
}

func TestContainerDefaults(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	bare := &v1alpha1.Container{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "default"}, Spec: v1alpha1.ContainerSpec{Image: "registry.test/web:1"}}
	createValid(t, c, bare)
	var fresh v1alpha1.Container
	if err := c.Get(ctx, client.ObjectKeyFromObject(bare), &fresh); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"requests.cpu":    fresh.Spec.Resources.Requests.CPU.String(),
		"requests.memory": fresh.Spec.Resources.Requests.Memory.String(),
		"limits.cpu":      fresh.Spec.Resources.Limits.CPU.String(),
		"limits.memory":   fresh.Spec.Resources.Limits.Memory.String(),
		"subnetRef":       fresh.Spec.Network.SubnetRef,
	}
	want := map[string]string{"requests.cpu": "100m", "requests.memory": "128Mi", "limits.cpu": "500m", "limits.memory": "512Mi", "subnetRef": "default"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if fresh.Spec.Replicas != nil || fresh.Spec.Autoscale != nil {
		t.Fatalf("replicas and autoscale have no default: %+v", fresh.Spec)
	}

	port := validContainer("port-default")
	port.Spec.Ports = []v1alpha1.ContainerPort{{Port: 53, Protocol: ""}}
	createValid(t, c, port)
	if err := c.Get(ctx, client.ObjectKeyFromObject(port), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.Ports[0].Protocol != "TCP" {
		t.Fatalf("protocol must default to TCP: %q", fresh.Spec.Ports[0].Protocol)
	}
}

func TestContainerValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	valid := validContainer("web")
	createValid(t, c, valid)
	valid.Status.Phase = "Running"
	valid.Status.Replicas = 2
	valid.Status.ReadyReplicas = 2
	valid.Status.Addresses = []string{"10.0.0.4", "10.0.0.5"}
	valid.Status.Deployment = "web"
	if err := c.Status().Update(ctx, valid); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	valid.Status.Phase = "Paused"
	requireInvalid(t, c.Status().Update(ctx, valid), "Unsupported value")

	stopped := validContainer("stopped")
	stopped.Spec.Replicas = ptr.To(int32(0))
	createValid(t, c, stopped)
	var fresh v1alpha1.Container
	if err := c.Get(ctx, client.ObjectKeyFromObject(stopped), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.Replicas == nil || *fresh.Spec.Replicas != 0 {
		t.Fatalf("replicas 0 must persist: %v", fresh.Spec.Replicas)
	}

	scaled := validContainer("scaled")
	scaled.Spec.Replicas = ptr.To(int32(20))
	createValid(t, c, scaled)

	autoscaled := validContainer("autoscaled")
	autoscaled.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 5, CPUPercent: ptr.To(int32(70))}
	createValid(t, c, autoscaled)

	pinned := validContainer("pinned")
	pinned.Spec.Network.Address = "10.0.0.50"
	pinned.Spec.Replicas = ptr.To(int32(1))
	pinned.Spec.Strategy = "Recreate"
	createValid(t, c, pinned)

	withVolume := validContainer("with-volume")
	withVolume.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
	createValid(t, c, withVolume)
}

func TestContainerRefusesWrongMixes(t *testing.T) {
	c, _ := StartTestEnv(t)
	cases := []struct {
		name   string
		mutate func(*v1alpha1.Container)
		want   string
	}{
		{"replicas-and-autoscale", func(x *v1alpha1.Container) {
			x.Spec.Replicas = ptr.To(int32(2))
			x.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 3}
		}, "replicas and autoscale exclude each other"},
		{"address-with-two-replicas", func(x *v1alpha1.Container) {
			x.Spec.Network.Address = "10.0.0.50"
			x.Spec.Replicas = ptr.To(int32(2))
		}, "address needs replicas of at most 1 and no autoscale"},
		{"address-with-autoscale", func(x *v1alpha1.Container) {
			x.Spec.Network.Address = "10.0.0.50"
			x.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 3}
		}, "address needs replicas of at most 1 and no autoscale"},
		{"address-not-ipv4", func(x *v1alpha1.Container) { x.Spec.Network.Address = "fd00::1" }, "address must be an IPv4 address"},
		{"volume-with-two-replicas", func(x *v1alpha1.Container) {
			x.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
			x.Spec.Replicas = ptr.To(int32(2))
		}, "volumes need replicas of at most 1 and no autoscale"},
		{"volume-with-autoscale", func(x *v1alpha1.Container) {
			x.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
			x.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 3}
		}, "volumes need replicas of at most 1 and no autoscale"},
		{"pinned-rolling-update", func(x *v1alpha1.Container) {
			x.Spec.Network.Address = "10.0.0.50"
			x.Spec.Strategy = "RollingUpdate"
		}, "address and volumes need the strategy Recreate"},
		{"volume-rolling-update", func(x *v1alpha1.Container) {
			x.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
			x.Spec.Strategy = "RollingUpdate"
		}, "address and volumes need the strategy Recreate"},
		{"too-many-replicas", func(x *v1alpha1.Container) { x.Spec.Replicas = ptr.To(int32(21)) }, "spec.replicas"},
		{"negative-replicas", func(x *v1alpha1.Container) { x.Spec.Replicas = ptr.To(int32(-1)) }, "spec.replicas"},
		{"unknown-strategy", func(x *v1alpha1.Container) { x.Spec.Strategy = "Blue" }, "Unsupported value"},
		{"empty-image", func(x *v1alpha1.Container) { x.Spec.Image = "" }, "spec.image"},
		{"too-many-ports", func(x *v1alpha1.Container) {
			x.Spec.Ports = nil
			for port := int32(1); port <= 9; port++ {
				x.Spec.Ports = append(x.Spec.Ports, v1alpha1.ContainerPort{Port: port, Protocol: "TCP"})
			}
		}, "spec.ports"},
		{"duplicate-port", func(x *v1alpha1.Container) {
			x.Spec.Ports = []v1alpha1.ContainerPort{{Port: 80, Protocol: "TCP"}, {Port: 80, Protocol: "TCP"}}
		}, "a port and protocol can be listed once"},
		{"port-zero", func(x *v1alpha1.Container) { x.Spec.Ports = []v1alpha1.ContainerPort{{Port: 0, Protocol: "TCP"}} }, "spec.ports"},
		{"port-protocol", func(x *v1alpha1.Container) { x.Spec.Ports = []v1alpha1.ContainerPort{{Port: 80, Protocol: "ICMP"}} }, "Unsupported value"},
		{"env-both", func(x *v1alpha1.Container) {
			x.Spec.Env = []v1alpha1.ContainerEnvVar{{Name: "A", Value: "1", ValueFrom: &v1alpha1.ContainerEnvSource{SecretKeyRef: &v1alpha1.ContainerSecretKeyRef{Name: "s", Key: "k"}}}}
		}, "set exactly one of value and valueFrom"},
		{"env-neither", func(x *v1alpha1.Container) { x.Spec.Env = []v1alpha1.ContainerEnvVar{{Name: "A"}} }, "set exactly one of value and valueFrom"},
		{"env-name", func(x *v1alpha1.Container) { x.Spec.Env = []v1alpha1.ContainerEnvVar{{Name: "1=A", Value: "1"}} }, "spec.env"},
		{"env-duplicate", func(x *v1alpha1.Container) {
			x.Spec.Env = []v1alpha1.ContainerEnvVar{{Name: "A", Value: "1"}, {Name: "A", Value: "2"}}
		}, "Duplicate value"},
		{"request-above-limit", func(x *v1alpha1.Container) {
			x.Spec.Resources.Requests.CPU = ptr.To(resource.MustParse("2"))
			x.Spec.Resources.Limits.CPU = ptr.To(resource.MustParse("1"))
		}, "requests must not be above limits"},
		{"memory-request-above-limit", func(x *v1alpha1.Container) {
			x.Spec.Resources.Requests.Memory = ptr.To(resource.MustParse("2Gi"))
			x.Spec.Resources.Limits.Memory = ptr.To(resource.MustParse("1Gi"))
		}, "requests must not be above limits"},
		{"limit-above-maximum", func(x *v1alpha1.Container) {
			x.Spec.Resources.Limits.CPU = ptr.To(resource.MustParse("33"))
		}, "limits.cpu must be at most 32"},
		{"probe-without-exec", func(x *v1alpha1.Container) { x.Spec.Probes.Liveness = &v1alpha1.ContainerProbe{} }, "only exec probes are supported"},
		{"empty-exec", func(x *v1alpha1.Container) {
			x.Spec.Probes.Readiness = &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{}}
		}, "spec.probes.readiness.exec.command"},
		{"autoscale-min-above-max", func(x *v1alpha1.Container) { x.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 4, Max: 3} }, "min must not be above max"},
		{"autoscale-max", func(x *v1alpha1.Container) { x.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 21} }, "spec.autoscale.max"},
		{"autoscale-percent", func(x *v1alpha1.Container) {
			x.Spec.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 3, CPUPercent: ptr.To(int32(101))}
		}, "spec.autoscale.cpuPercent"},
		{"too-many-volumes", func(x *v1alpha1.Container) {
			for i := 0; i < 9; i++ {
				x.Spec.Volumes = append(x.Spec.Volumes, v1alpha1.ContainerVolume{VolumeRef: "v" + string(rune('a'+i)), MountPath: "/m" + string(rune('a'+i))})
			}
		}, "spec.volumes"},
		{"duplicate-volume", func(x *v1alpha1.Container) {
			x.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/a"}, {VolumeRef: "data", MountPath: "/b"}}
		}, "a volume can be listed once"},
		{"duplicate-mount-path", func(x *v1alpha1.Container) {
			x.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "one", MountPath: "/a"}, {VolumeRef: "two", MountPath: "/a"}}
		}, "a mount path can be used once"},
		{"relative-mount-path", func(x *v1alpha1.Container) {
			x.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "data"}}
		}, "spec.volumes[0].mountPath"},
		{"too-many-groups", func(x *v1alpha1.Container) {
			x.Spec.Network.SecurityGroups = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, "spec.network.securityGroups"},
		{"name-too-long", func(x *v1alpha1.Container) { x.Name = strings.Repeat("a", 53) }, "the name must have at most 52 characters"},
		{"name-uppercase", func(x *v1alpha1.Container) { x.Name = "Web" }, "the name must be a DNS label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := validContainer("mix-" + tc.name)
			tc.mutate(x)
			createInvalid(t, c, x, tc.want)
		})
	}
}

func TestContainerNetworkIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	pinned := validContainer("immutable")
	pinned.Spec.Network.Address = "10.0.0.60"
	pinned.Spec.Replicas = ptr.To(int32(1))
	createValid(t, c, pinned)

	var fresh v1alpha1.Container
	get := func() *v1alpha1.Container {
		if err := c.Get(ctx, client.ObjectKeyFromObject(pinned), &fresh); err != nil {
			t.Fatal(err)
		}
		return fresh.DeepCopy()
	}
	moved := get()
	moved.Spec.Network.Address = "10.0.0.61"
	updateInvalid(t, c, moved, "address is immutable")
	cleared := get()
	cleared.Spec.Network.Address = ""
	updateInvalid(t, c, cleared, "address is immutable")
	elsewhere := get()
	elsewhere.Spec.Network.SubnetRef = "other"
	updateInvalid(t, c, elsewhere, "subnetRef is immutable")
	image := get()
	image.Spec.Image = "registry.test/web:2"
	if err := c.Update(ctx, image); err != nil {
		t.Fatalf("the image must change in place: %v", err)
	}

	free := validContainer("free-address")
	createValid(t, c, free)
	assigned := &v1alpha1.Container{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(free), assigned); err != nil {
		t.Fatal(err)
	}
	assigned.Spec.Network.Address = "10.0.0.62"
	updateInvalid(t, c, assigned, "address is immutable")
}
