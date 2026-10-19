package container

import (
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func baseBuildInput() BuildInput {
	return BuildInput{
		Build: v1alpha1.Build{
			ObjectMeta: metav1.ObjectMeta{Name: "shop-1", Namespace: "t1-prod", UID: "uid-9"},
			Spec: v1alpha1.BuildSpec{
				Type: v1alpha1.BuildTypeDockerfile,
				Source: v1alpha1.BuildSource{Git: v1alpha1.BuildGitSource{
					URL:       "https://git.test/acme/shop.git",
					Revision:  "0123456789abcdef",
					SecretRef: "git-credentials",
				}},
				ContextPath:    ".",
				DockerfilePath: "Dockerfile",
				Output:         v1alpha1.BuildOutput{Image: "registry.test/acme/shop:1", PushSecretRef: "registry-credentials"},
				Size:           v1alpha1.BuildSizeMedium,
				Network:        v1alpha1.BuildNetwork{SubnetRef: "default"},
			},
		},
		Labels:        v1alpha1.TenantLabels("Build", "shop-1", "t1", "prod"),
		NIC:           network.NIC{Namespace: "t1-prod", Subnet: "default"},
		DNSServers:    []string{"10.0.0.2", "10.0.0.3"},
		GitImage:      "alpine/git:v2.54.0",
		BuildKitImage: "moby/buildkit:v0.31.2",
		BuilderImage:  "paketobuildpacks/builder-jammy-base:0.4.640",
		RunImage:      "paketobuildpacks/run-jammy-base:0.1.275",
		Timeout:       30 * time.Minute,
		JobTTL:        time.Hour,
	}
}

func withBuildSpec(change func(*v1alpha1.BuildSpec)) BuildInput {
	in := baseBuildInput()
	change(&in.Build.Spec)
	return in
}

func mustRenderBuild(t *testing.T, in BuildInput) (batchv1.Job, Rendered) {
	t.Helper()
	rendered, err := RenderBuild(in)
	if err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if err := runtime.DefaultUnstructuredConverter.FromUnstructuredWithValidation(rendered.Job.Object, &job, true); err != nil {
		t.Fatal(err)
	}
	return job, rendered
}

func containerNamed(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no container %q in %v", name, containers)
	return corev1.Container{}
}

func TestRenderBuildJobAndPodFields(t *testing.T) {
	job, rendered := mustRenderBuild(t, baseBuildInput())
	if rendered.ConfigMap != nil {
		t.Fatalf("no mirrors, no ConfigMap: %v", rendered.ConfigMap)
	}
	if job.Name != "shop-1" || job.Namespace != "t1-prod" {
		t.Fatalf("job identity %s/%s", job.Namespace, job.Name)
	}
	if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 1800 || *job.Spec.TTLSecondsAfterFinished != 3600 {
		t.Fatalf("job limits %d %d %d", *job.Spec.BackoffLimit, *job.Spec.ActiveDeadlineSeconds, *job.Spec.TTLSecondsAfterFinished)
	}
	owner := job.OwnerReferences
	if len(owner) != 1 || owner[0].Kind != "Build" || owner[0].Name != "shop-1" || owner[0].UID != "uid-9" || !*owner[0].Controller || !*owner[0].BlockOwnerDeletion {
		t.Fatalf("owner %+v", owner)
	}
	if job.Labels[v1alpha1.LabelKind] != "Build" || job.Labels[v1alpha1.LabelTenant] != "t1" || job.Labels[v1alpha1.LabelProject] != "prod" {
		t.Fatalf("labels %v", job.Labels)
	}

	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restartPolicy %s", pod.RestartPolicy)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("the service account token must not be mounted")
	}
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "kata" {
		t.Fatalf("runtimeClassName %v", pod.RuntimeClassName)
	}
	if pod.SecurityContext == nil || *pod.SecurityContext.FSGroup != 1000 {
		t.Fatalf("pod security context %+v", pod.SecurityContext)
	}
	if pod.DNSPolicy != corev1.DNSNone || !reflect.DeepEqual(pod.DNSConfig.Nameservers, []string{"10.0.0.2", "10.0.0.3"}) {
		t.Fatalf("dns %s %+v", pod.DNSPolicy, pod.DNSConfig)
	}
	if !reflect.DeepEqual(pod.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "registry-credentials"}}) {
		t.Fatalf("imagePullSecrets %v", pod.ImagePullSecrets)
	}
	podMeta := job.Spec.Template.ObjectMeta
	wantAttachment := network.PodAttachment(network.NIC{Namespace: "t1-prod", Subnet: "default"})
	for key, value := range wantAttachment.Annotations {
		if podMeta.Annotations[key] != value {
			t.Errorf("pod annotation %s = %q, want %q", key, podMeta.Annotations[key], value)
		}
	}
	for key, value := range wantAttachment.Labels {
		if podMeta.Labels[key] != value {
			t.Errorf("pod label %s = %q, want %q", key, podMeta.Labels[key], value)
		}
	}
	if podMeta.Labels[v1alpha1.LabelKind] != "Build" || podMeta.Labels[v1alpha1.LabelName] != "shop-1" {
		t.Fatalf("pod selector labels %v", podMeta.Labels)
	}
}

func TestRenderBuildWithoutDNSServersKeepsTheDefault(t *testing.T) {
	in := baseBuildInput()
	in.DNSServers = nil
	job, _ := mustRenderBuild(t, in)
	pod := job.Spec.Template.Spec
	if pod.DNSPolicy != "" || pod.DNSConfig != nil {
		t.Fatalf("dns %s %+v", pod.DNSPolicy, pod.DNSConfig)
	}
}

func TestBuildctlArgsWithoutCache(t *testing.T) {
	job, _ := mustRenderBuild(t, baseBuildInput())
	build := containerNamed(t, job.Spec.Template.Spec.Containers, "build")
	if build.Image != "moby/buildkit:v0.31.2" || !reflect.DeepEqual(build.Command, []string{"buildctl-daemonless.sh"}) {
		t.Fatalf("image %s command %v", build.Image, build.Command)
	}
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/workspace",
		"--local", "dockerfile=/workspace",
		"--opt", "filename=Dockerfile",
		"--output", "type=image,name=registry.test/acme/shop:1,push=true",
	}
	if !reflect.DeepEqual(build.Args, want) {
		t.Fatalf("args\n got %v\nwant %v", build.Args, want)
	}
}

func TestBuildctlArgsWithCacheAndPaths(t *testing.T) {
	in := withBuildSpec(func(spec *v1alpha1.BuildSpec) {
		spec.Cache.Enabled = true
		spec.ContextPath = "services/api"
		spec.DockerfilePath = "services/api/docker/Dockerfile.prod"
		spec.Output.Image = "registry.test:5000/acme/shop:7"
	})
	job, _ := mustRenderBuild(t, in)
	build := containerNamed(t, job.Spec.Template.Spec.Containers, "build")
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/workspace/services/api",
		"--local", "dockerfile=/workspace/services/api/docker",
		"--opt", "filename=Dockerfile.prod",
		"--output", "type=image,name=registry.test:5000/acme/shop:7,push=true",
		"--export-cache", "type=registry,ref=registry.test:5000/acme/shop:cache,mode=max",
		"--import-cache", "type=registry,ref=registry.test:5000/acme/shop:cache",
	}
	if !reflect.DeepEqual(build.Args, want) {
		t.Fatalf("args\n got %v\nwant %v", build.Args, want)
	}
}

func TestBuildctlArgsUseDefaultsForEmptyPaths(t *testing.T) {
	in := withBuildSpec(func(spec *v1alpha1.BuildSpec) {
		spec.ContextPath = ""
		spec.DockerfilePath = ""
	})
	job, _ := mustRenderBuild(t, in)
	args := strings.Join(containerNamed(t, job.Spec.Template.Spec.Containers, "build").Args, " ")
	for _, want := range []string{"context=/workspace ", "dockerfile=/workspace ", "filename=Dockerfile "} {
		if !strings.Contains(args+" ", want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
}

func TestCacheImage(t *testing.T) {
	cases := map[string]string{
		"registry.test/acme/shop:1":      "registry.test/acme/shop:cache",
		"registry.test/acme/shop":        "registry.test/acme/shop:cache",
		"registry.test:5000/acme/shop":   "registry.test:5000/acme/shop:cache",
		"registry.test:5000/acme/shop:2": "registry.test:5000/acme/shop:cache",
	}
	for image, want := range cases {
		if got := CacheImage(image); got != want {
			t.Errorf("CacheImage(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestGitCloneRunsFirstAsNonRootWithoutTheTokenInTheScriptOrURL(t *testing.T) {
	job, _ := mustRenderBuild(t, baseBuildInput())
	pod := job.Spec.Template.Spec
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "git-clone" {
		t.Fatalf("init containers %v", pod.InitContainers)
	}
	clone := pod.InitContainers[0]
	if clone.Image != "alpine/git:v2.54.0" {
		t.Fatalf("image %s", clone.Image)
	}
	sc := clone.SecurityContext
	if sc == nil || !*sc.RunAsNonRoot || *sc.RunAsUser != 1001 || *sc.RunAsGroup != 1000 || *sc.AllowPrivilegeEscalation || (sc.Privileged != nil && *sc.Privileged) {
		t.Fatalf("git-clone security context %+v", sc)
	}
	env := map[string]corev1.EnvVar{}
	for _, e := range clone.Env {
		env[e.Name] = e
	}
	if env["GIT_URL"].Value != "https://git.test/acme/shop.git" || env["GIT_REVISION"].Value != "0123456789abcdef" {
		t.Fatalf("env %v", clone.Env)
	}
	for name, key := range map[string]string{"GIT_USERNAME": "username", "GIT_TOKEN": "token"} {
		ref := env[name].ValueFrom
		if ref == nil || ref.SecretKeyRef == nil || ref.SecretKeyRef.Name != "git-credentials" || ref.SecretKeyRef.Key != key {
			t.Errorf("%s = %+v", name, env[name])
		}
	}
	script := strings.Join(clone.Command, " ")
	if !strings.Contains(script, "http.${GIT_URL}.extraHeader") || !strings.Contains(script, "git clone --no-checkout") || !strings.Contains(script, "--end-of-options") || !strings.Contains(script, "git checkout --detach \"$resolved\" --") {
		t.Fatalf("script %q", script)
	}
	if strings.Contains(script, "git.test") || strings.Contains(script, "@") {
		t.Fatalf("the script must not hold the URL or credentials: %q", script)
	}
}

func TestGitCloneWithoutSecretHasNoCredentialEnv(t *testing.T) {
	in := withBuildSpec(func(spec *v1alpha1.BuildSpec) { spec.Source.Git.SecretRef = "" })
	job, _ := mustRenderBuild(t, in)
	for _, e := range job.Spec.Template.Spec.InitContainers[0].Env {
		if e.ValueFrom != nil {
			t.Errorf("env %s must not come from a Secret", e.Name)
		}
	}
}

func TestTheGitTokenReachesOnlyTheCloneContainer(t *testing.T) {
	for _, in := range allBuildInputs() {
		job, rendered := mustRenderBuild(t, in)
		pod := job.Spec.Template.Spec
		for _, c := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			for _, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && c.Name != "git-clone" {
					t.Errorf("%s: container %s reads the Secret %s through the env %s", in.Build.Spec.Type, c.Name, e.ValueFrom.SecretKeyRef.Name, e.Name)
				}
			}
			for _, from := range c.EnvFrom {
				t.Errorf("container %s uses envFrom %+v", c.Name, from)
			}
		}
		for _, volume := range pod.Volumes {
			if volume.Secret != nil && volume.Secret.SecretName != in.Build.Spec.Output.PushSecretRef {
				t.Errorf("volume %s mounts the Secret %s", volume.Name, volume.Secret.SecretName)
			}
			if volume.Projected != nil {
				t.Errorf("volume %s is projected", volume.Name)
			}
		}
		if secret := in.Build.Spec.Source.Git.SecretRef; secret != "" {
			if count := strings.Count(toJSON(t, rendered.Job.Object), "\""+secret+"\""); count != 2 {
				t.Errorf("the git Secret is named %d times, want 2 (username and token)", count)
			}
		}
	}
}

func TestPushSecretIsMountedAsDockerConfigInTheBuildContainer(t *testing.T) {
	job, _ := mustRenderBuild(t, baseBuildInput())
	pod := job.Spec.Template.Spec
	var push *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Secret != nil {
			push = &pod.Volumes[i]
		}
	}
	if push == nil || push.Secret.SecretName != "registry-credentials" {
		t.Fatalf("volumes %+v", pod.Volumes)
	}
	if !reflect.DeepEqual(push.Secret.Items, []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: "config.json"}}) {
		t.Fatalf("items %+v", push.Secret.Items)
	}
	build := containerNamed(t, pod.Containers, "build")
	var mounted bool
	for _, m := range build.VolumeMounts {
		if m.Name == push.Name {
			mounted = m.MountPath == "/docker-config" && m.ReadOnly
		}
	}
	if !mounted {
		t.Fatalf("mounts %+v", build.VolumeMounts)
	}
	envs := map[string]string{}
	for _, e := range build.Env {
		envs[e.Name] = e.Value
	}
	if envs["DOCKER_CONFIG"] != "/docker-config" {
		t.Fatalf("env %v", envs)
	}
	if _, set := envs["BUILDKITD_FLAGS"]; set {
		t.Fatalf("no mirrors, no BUILDKITD_FLAGS: %v", envs)
	}
	for _, m := range pod.InitContainers[0].VolumeMounts {
		if m.Name == push.Name {
			t.Fatal("git-clone must not mount the push credential")
		}
	}
}

func TestBuildVolumesUseTheTierScratchLimit(t *testing.T) {
	for size, scratch := range map[string]string{"s": "5Gi", "m": "10Gi", "l": "20Gi"} {
		in := withBuildSpec(func(spec *v1alpha1.BuildSpec) { spec.Size = size })
		job, _ := mustRenderBuild(t, in)
		limits := map[string]resource.Quantity{}
		for _, volume := range job.Spec.Template.Spec.Volumes {
			if volume.EmptyDir != nil {
				if volume.EmptyDir.Medium != "" {
					t.Errorf("%s: volume %s uses the medium %q", size, volume.Name, volume.EmptyDir.Medium)
				}
				limits[volume.Name] = *volume.EmptyDir.SizeLimit
			}
		}
		for _, name := range []string{"workspace", "layers", "buildkit-state"} {
			limit, found := limits[name]
			if !found || limit.Cmp(resource.MustParse(scratch)) != 0 {
				t.Errorf("%s: volume %s limit %v, want %s", size, name, limit.String(), scratch)
			}
		}
		build := containerNamed(t, job.Spec.Template.Spec.Containers, "build")
		var state bool
		for _, m := range build.VolumeMounts {
			state = state || (m.Name == "buildkit-state" && m.MountPath == "/var/lib/buildkit")
		}
		if !state {
			t.Errorf("%s: the build container lacks the buildkit state mount: %+v", size, build.VolumeMounts)
		}
	}
}

func TestBuildTierResources(t *testing.T) {
	cases := []struct {
		size                                              string
		cpuRequest, cpuLimit, memRequest, memLimit, store string
	}{
		{"s", "500m", "1", "1Gi", "2Gi", "5Gi"},
		{"m", "1", "2", "2Gi", "4Gi", "10Gi"},
		{"l", "2", "4", "4Gi", "8Gi", "20Gi"},
		{"", "1", "2", "2Gi", "4Gi", "10Gi"},
		{"xl", "1", "2", "2Gi", "4Gi", "10Gi"},
	}
	for _, tc := range cases {
		in := withBuildSpec(func(spec *v1alpha1.BuildSpec) { spec.Size = tc.size })
		job, _ := mustRenderBuild(t, in)
		pod := job.Spec.Template.Spec
		for _, c := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			want := map[string]map[corev1.ResourceName]string{
				"requests": {corev1.ResourceCPU: tc.cpuRequest, corev1.ResourceMemory: tc.memRequest, corev1.ResourceEphemeralStorage: tc.store},
				"limits":   {corev1.ResourceCPU: tc.cpuLimit, corev1.ResourceMemory: tc.memLimit, corev1.ResourceEphemeralStorage: tc.store},
			}
			for kind, list := range map[string]corev1.ResourceList{"requests": c.Resources.Requests, "limits": c.Resources.Limits} {
				for name, value := range want[kind] {
					got := list[name]
					if got.Cmp(resource.MustParse(value)) != 0 {
						t.Errorf("size %q container %s %s %s = %s, want %s", tc.size, c.Name, kind, name, got.String(), value)
					}
				}
			}
		}
	}
}

func allBuildInputs() []BuildInput {
	var inputs []BuildInput
	for _, buildType := range []string{v1alpha1.BuildTypeDockerfile, v1alpha1.BuildTypeBuildpacks} {
		for _, size := range []string{"", "s", "m", "l"} {
			for _, cache := range []bool{false, true} {
				for _, mirrors := range [][]settings.RegistryMirror{nil, {{Upstream: "docker.io", Target: "r.test/d"}}} {
					for _, secret := range []string{"", "git-credentials"} {
						in := baseBuildInput()
						in.Build.Spec.Type = buildType
						in.Build.Spec.Size = size
						in.Build.Spec.Cache.Enabled = cache
						in.Build.Spec.Source.Git.SecretRef = secret
						in.Mirrors = mirrors
						inputs = append(inputs, in)
					}
				}
			}
		}
	}
	return inputs
}

func TestAPrivilegedContainerAlwaysComesWithKata(t *testing.T) {
	rendered := 0
	privileged := 0
	for _, in := range allBuildInputs() {
		rendered++
		job, err := RenderBuild(in)
		if err != nil {
			continue
		}
		var parsed batchv1.Job
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(job.Job.Object, &parsed); err != nil {
			t.Fatal(err)
		}
		pod := parsed.Spec.Template.Spec
		for _, c := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
				privileged++
				if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "kata" {
					t.Fatalf("type %s size %q: container %s is privileged without runtimeClassName kata", in.Build.Spec.Type, in.Build.Spec.Size, c.Name)
				}
			}
		}
	}
	if rendered == 0 || privileged == 0 {
		t.Fatalf("the property ran on %d inputs and saw %d privileged containers", rendered, privileged)
	}
}

func TestOnlyTheBuildContainerIsPrivileged(t *testing.T) {
	job, _ := mustRenderBuild(t, baseBuildInput())
	pod := job.Spec.Template.Spec
	for _, c := range pod.InitContainers {
		if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
			t.Errorf("init container %s is privileged", c.Name)
		}
	}
	build := containerNamed(t, pod.Containers, "build")
	if !*build.SecurityContext.Privileged || *build.SecurityContext.RunAsUser != 0 {
		t.Fatalf("build security context %+v", build.SecurityContext)
	}
}

func TestMirrorsRenderASortedConfigMapAndMountIt(t *testing.T) {
	in := baseBuildInput()
	in.Mirrors = []settings.RegistryMirror{
		{Upstream: "quay.io", Target: "r.test/mirror-quay"},
		{Upstream: "docker.io", Target: "r.test/mirror-docker"},
		{Upstream: "ghcr.io", Target: "r.test/mirror-ghcr"},
	}
	job, rendered := mustRenderBuild(t, in)
	cm := rendered.ConfigMap
	if cm == nil || cm.GetName() != "shop-1-buildkitd" || cm.GetNamespace() != "t1-prod" || cm.GetKind() != "ConfigMap" {
		t.Fatalf("config map %v", cm)
	}
	owners := cm.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Build" || owners[0].UID != "uid-9" {
		t.Fatalf("owners %v", owners)
	}
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	want := `[registry."docker.io"]
  mirrors = ["r.test/mirror-docker"]
[registry."ghcr.io"]
  mirrors = ["r.test/mirror-ghcr"]
[registry."quay.io"]
  mirrors = ["r.test/mirror-quay"]
`
	if data["buildkitd.toml"] != want || len(data) != 1 {
		t.Fatalf("data\n%v\nwant\n%s", data, want)
	}
	build := containerNamed(t, job.Spec.Template.Spec.Containers, "build")
	flags := ""
	for _, e := range build.Env {
		if e.Name == "BUILDKITD_FLAGS" {
			flags = e.Value
		}
	}
	if flags != "--config /etc/buildkit/buildkitd.toml" {
		t.Fatalf("BUILDKITD_FLAGS %q", flags)
	}
	var mount corev1.VolumeMount
	for _, m := range build.VolumeMounts {
		if m.MountPath == "/etc/buildkit/buildkitd.toml" {
			mount = m
		}
	}
	if mount.SubPath != "buildkitd.toml" || !mount.ReadOnly {
		t.Fatalf("mount %+v", mount)
	}
	var found bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == mount.Name {
			found = v.ConfigMap != nil && v.ConfigMap.Name == "shop-1-buildkitd"
		}
	}
	if !found {
		t.Fatalf("volumes %+v", job.Spec.Template.Spec.Volumes)
	}
}

func TestBuildkitdConfigGroupsSeveralMirrorsOfOneUpstream(t *testing.T) {
	got := BuildkitdConfig([]settings.RegistryMirror{
		{Upstream: "docker.io", Target: "r.test/b"},
		{Upstream: "docker.io", Target: "r.test/a"},
	})
	want := "[registry.\"docker.io\"]\n  mirrors = [\"r.test/a\", \"r.test/b\"]\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderBuildDoesNotChangeItsInput(t *testing.T) {
	in := baseBuildInput()
	in.Mirrors = []settings.RegistryMirror{{Upstream: "b", Target: "x/y"}, {Upstream: "a", Target: "x/z"}}
	before := baseBuildInput()
	before.Mirrors = []settings.RegistryMirror{{Upstream: "b", Target: "x/y"}, {Upstream: "a", Target: "x/z"}}
	if _, err := RenderBuild(in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("the input changed:\n%+v\n%+v", in, before)
	}
}

func TestRenderBuildRefusesAnUnknownType(t *testing.T) {
	in := withBuildSpec(func(spec *v1alpha1.BuildSpec) { spec.Type = "nix" })
	if _, err := RenderBuild(in); err == nil {
		t.Fatal("an unknown type must fail")
	}
}

func toJSON(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := runtime.Encode(unstructured.UnstructuredJSONScheme, &unstructured.Unstructured{Object: value})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEveryBuildContainerKeepsTheEndOfItsLogAsTheTerminationMessage(t *testing.T) {
	for _, in := range allBuildInputs() {
		job, _ := mustRenderBuild(t, in)
		pod := job.Spec.Template.Spec
		for _, c := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
				t.Errorf("%s %s: policy %q", in.Build.Spec.Type, c.Name, c.TerminationMessagePolicy)
			}
		}
	}
}
