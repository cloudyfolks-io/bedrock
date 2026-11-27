package container

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func cnbBuildInput(change func(*BuildInput)) BuildInput {
	in := baseBuildInput()
	in.Build.Spec.Type = v1alpha1.BuildTypeBuildpacks
	change(&in)
	return in
}

func phaseContainers(t *testing.T, in BuildInput) map[string]corev1.Container {
	t.Helper()
	job, _ := mustRenderBuild(t, in)
	pod := job.Spec.Template.Spec
	out := map[string]corev1.Container{}
	for _, c := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
		out[c.Name] = c
	}
	return out
}

func TestBuildpacksPhasesRunInOrderWithTheExporterLast(t *testing.T) {
	job, _ := mustRenderBuild(t, cnbBuildInput(func(*BuildInput) {}))
	pod := job.Spec.Template.Spec
	var inits []string
	for _, c := range pod.InitContainers {
		inits = append(inits, c.Name)
	}
	if !reflect.DeepEqual(inits, []string{"git-clone", "analyzer", "detector", "restorer", "builder"}) {
		t.Fatalf("init containers %v", inits)
	}
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "exporter" {
		t.Fatalf("containers %v", pod.Containers)
	}
	for _, c := range pod.InitContainers[1:] {
		if c.Image != "paketobuildpacks/builder-jammy-base:0.4.640" || !reflect.DeepEqual(c.Command, []string{"/cnb/lifecycle/" + c.Name}) {
			t.Errorf("%s: image %s command %v", c.Name, c.Image, c.Command)
		}
	}
	if pod.Containers[0].Image != "paketobuildpacks/builder-jammy-base:0.4.640" || !reflect.DeepEqual(pod.Containers[0].Command, []string{"/cnb/lifecycle/exporter"}) {
		t.Fatalf("exporter %+v", pod.Containers[0])
	}
}

func TestBuildpacksArgsWithoutCache(t *testing.T) {
	phases := phaseContainers(t, cnbBuildInput(func(*BuildInput) {}))
	want := map[string][]string{
		"analyzer": {"-layers=/layers", "-analyzed=/layers/analyzed.toml", "-run-image=paketobuildpacks/run-jammy-base:0.1.275", "registry.test/acme/shop:1"},
		"detector": {"-layers=/layers", "-app=/workspace", "-group=/layers/group.toml", "-plan=/layers/plan.toml"},
		"restorer": {"-layers=/layers"},
		"builder":  {"-layers=/layers", "-app=/workspace", "-group=/layers/group.toml", "-plan=/layers/plan.toml"},
		"exporter": {"-layers=/layers", "-app=/workspace", "-analyzed=/layers/analyzed.toml", "registry.test/acme/shop:1"},
	}
	for name, args := range want {
		if !reflect.DeepEqual(phases[name].Args, args) {
			t.Errorf("%s args\n got %v\nwant %v", name, phases[name].Args, args)
		}
	}
}

func TestBuildpacksArgsWithCacheAndContextPath(t *testing.T) {
	phases := phaseContainers(t, cnbBuildInput(func(in *BuildInput) {
		in.Build.Spec.Cache.Enabled = true
		in.Build.Spec.ContextPath = "services/api"
		in.Build.Spec.Output.Image = "registry.test:5000/acme/shop:7"
	}))
	want := map[string][]string{
		"analyzer": {"-layers=/layers", "-analyzed=/layers/analyzed.toml", "-run-image=paketobuildpacks/run-jammy-base:0.1.275", "registry.test:5000/acme/shop:7"},
		"detector": {"-layers=/layers", "-app=/workspace/services/api", "-group=/layers/group.toml", "-plan=/layers/plan.toml"},
		"restorer": {"-layers=/layers", "-cache-image=registry.test:5000/acme/shop:cache"},
		"builder":  {"-layers=/layers", "-app=/workspace/services/api", "-group=/layers/group.toml", "-plan=/layers/plan.toml"},
		"exporter": {"-layers=/layers", "-app=/workspace/services/api", "-analyzed=/layers/analyzed.toml", "registry.test:5000/acme/shop:7", "-cache-image=registry.test:5000/acme/shop:cache"},
	}
	for name, args := range want {
		if !reflect.DeepEqual(phases[name].Args, args) {
			t.Errorf("%s args\n got %v\nwant %v", name, phases[name].Args, args)
		}
	}
}

func TestBuildpacksRunImageGoesToTheAnalyzerOnly(t *testing.T) {
	for name, c := range phaseContainers(t, cnbBuildInput(func(*BuildInput) {})) {
		for _, arg := range c.Args {
			if len(arg) > 11 && arg[:11] == "-run-image=" && name != "analyzer" {
				t.Errorf("%s has %s", name, arg)
			}
		}
	}
	noRun := phaseContainers(t, cnbBuildInput(func(in *BuildInput) { in.RunImage = "" }))
	for _, arg := range noRun["analyzer"].Args {
		if len(arg) > 11 && arg[:11] == "-run-image=" {
			t.Fatalf("an empty run image sets no flag: %v", noRun["analyzer"].Args)
		}
	}
}

func hasPushAuth(c corev1.Container) bool {
	env, mount := false, false
	for _, e := range c.Env {
		env = env || (e.Name == "DOCKER_CONFIG" && e.Value == "/docker-config")
	}
	for _, m := range c.VolumeMounts {
		mount = mount || (m.Name == "push-credentials" && m.MountPath == "/docker-config" && m.ReadOnly)
	}
	if env != mount {
		return false
	}
	return env
}

func TestBuildpacksRegistryAuthReachesOnlyThePhasesThatNeedIt(t *testing.T) {
	withoutCache := phaseContainers(t, cnbBuildInput(func(*BuildInput) {}))
	withCache := phaseContainers(t, cnbBuildInput(func(in *BuildInput) { in.Build.Spec.Cache.Enabled = true }))
	wantWithout := map[string]bool{"git-clone": false, "analyzer": true, "detector": false, "restorer": false, "builder": false, "exporter": true}
	wantWith := map[string]bool{"git-clone": false, "analyzer": true, "detector": false, "restorer": true, "builder": false, "exporter": true}
	for name, want := range wantWithout {
		if got := hasPushAuth(withoutCache[name]); got != want {
			t.Errorf("no cache, %s: auth %v, want %v", name, got, want)
		}
	}
	for name, want := range wantWith {
		if got := hasPushAuth(withCache[name]); got != want {
			t.Errorf("cache, %s: auth %v, want %v", name, got, want)
		}
	}
}

func TestBuildpacksPhasesShareTheLayersAndSetThePlatformAPI(t *testing.T) {
	phases := phaseContainers(t, cnbBuildInput(func(*BuildInput) {}))
	for name, c := range phases {
		if name == "git-clone" {
			continue
		}
		var layers, workspace bool
		for _, m := range c.VolumeMounts {
			layers = layers || (m.Name == "layers" && m.MountPath == "/layers")
			workspace = workspace || (m.Name == "workspace" && m.MountPath == "/workspace")
		}
		var api string
		for _, e := range c.Env {
			if e.Name == "CNB_PLATFORM_API" {
				api = e.Value
			}
		}
		if !layers || !workspace || api != "0.7" || c.WorkingDir != "/workspace" {
			t.Errorf("%s: layers %v workspace %v api %q dir %q", name, layers, workspace, api, c.WorkingDir)
		}
	}
}

func TestBuildpacksRunNonRootAndNeverPrivilegedInsideKata(t *testing.T) {
	job, rendered := mustRenderBuild(t, cnbBuildInput(func(in *BuildInput) {
		in.Mirrors = []settings.RegistryMirror{{Upstream: "docker.io", Target: "r.test/d"}}
	}))
	pod := job.Spec.Template.Spec
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "kata" {
		t.Fatalf("runtimeClassName %v", pod.RuntimeClassName)
	}
	for _, c := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
		sc := c.SecurityContext
		if sc == nil || !*sc.RunAsNonRoot || *sc.RunAsUser != 1001 || *sc.RunAsGroup != 1000 || *sc.AllowPrivilegeEscalation || (sc.Privileged != nil && *sc.Privileged) {
			t.Errorf("%s: security context %+v", c.Name, sc)
		}
	}
	if rendered.ConfigMap != nil {
		t.Fatal("registry mirrors serve BuildKit FROM lines only; a buildpacks build has no config map")
	}
	for _, volume := range pod.Volumes {
		if volume.Name == "buildkit-state" || volume.Name == "buildkitd-config" {
			t.Errorf("volume %s belongs to the Dockerfile build", volume.Name)
		}
	}
	if pod.SecurityContext == nil || *pod.SecurityContext.FSGroup != 1000 {
		t.Fatalf("pod security context %+v", pod.SecurityContext)
	}
}

func TestBuildpacksResourcesFollowTheTierOnEveryContainer(t *testing.T) {
	for size, cpu := range map[string]string{"s": "1", "m": "2", "l": "4"} {
		phases := phaseContainers(t, cnbBuildInput(func(in *BuildInput) { in.Build.Spec.Size = size }))
		if len(phases) != 6 {
			t.Fatalf("containers %d", len(phases))
		}
		for name, c := range phases {
			if got := c.Resources.Limits[corev1.ResourceCPU]; got.Cmp(resource.MustParse(cpu)) != 0 {
				t.Errorf("%s size %s: cpu limit %s", name, size, got.String())
			}
		}
	}
}

func TestBuildpacksKeepTheGitTokenInTheCloneContainer(t *testing.T) {
	phases := phaseContainers(t, cnbBuildInput(func(*BuildInput) {}))
	for name, c := range phases {
		for _, e := range c.Env {
			secret := e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil
			if secret != (name == "git-clone" && (e.Name == "GIT_USERNAME" || e.Name == "GIT_TOKEN")) {
				t.Errorf("%s: env %s from a Secret = %v", name, e.Name, secret)
			}
		}
	}
}
