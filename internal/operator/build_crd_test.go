package operator

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func validBuild(name string) *v1alpha1.Build {
	return &v1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.BuildSpec{
			Source: v1alpha1.BuildSource{Git: v1alpha1.BuildGitSource{
				URL:       "https://git.test/acme/shop.git",
				Revision:  "0123456789abcdef0123456789abcdef01234567",
				SecretRef: "git-credentials",
			}},
			Output:  v1alpha1.BuildOutput{Image: "registry.test/acme/shop:1", PushSecretRef: "registry-credentials"},
			Network: v1alpha1.BuildNetwork{SubnetRef: "default"},
		},
	}
}

func TestBuildDefaults(t *testing.T) {
	c, _ := StartTestEnv(t)
	build := validBuild("defaults")
	createValid(t, c, build)
	var fresh v1alpha1.Build
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(build), &fresh); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"type":           fresh.Spec.Type,
		"size":           fresh.Spec.Size,
		"contextPath":    fresh.Spec.ContextPath,
		"dockerfilePath": fresh.Spec.DockerfilePath,
	}
	want := map[string]string{"type": "dockerfile", "size": "m", "contextPath": ".", "dockerfilePath": "Dockerfile"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if fresh.Spec.Cache.Enabled || fresh.Spec.Timeout != nil {
		t.Fatalf("cache and timeout have no default: %+v", fresh.Spec)
	}
}

func TestBuildAcceptsEveryChoice(t *testing.T) {
	c, _ := StartTestEnv(t)
	buildpacks := validBuild("buildpacks")
	buildpacks.Spec.Type = "buildpacks"
	buildpacks.Spec.Size = "l"
	buildpacks.Spec.Cache.Enabled = true
	buildpacks.Spec.Timeout = &metav1.Duration{Duration: 45 * time.Minute}
	buildpacks.Spec.ContextPath = "services/api"
	buildpacks.Spec.DockerfilePath = "build/Dockerfile.prod"
	createValid(t, c, buildpacks)

	public := validBuild("public")
	public.Spec.Source.Git.SecretRef = ""
	public.Spec.Source.Git.Revision = "refs/heads/main"
	createValid(t, c, public)

	byAddress := validBuild("by-address")
	byAddress.Spec.Source.Git.URL = "https://10.100.0.221/dockerfile.git"
	byAddress.Spec.Output.Image = "10.100.0.220:5000/lab/docker:1"
	createValid(t, c, byAddress)

	for _, size := range []string{"s", "m", "l"} {
		sized := validBuild("size-" + size)
		sized.Spec.Size = size
		createValid(t, c, sized)
	}
}

func TestBuildRefusesWrongValues(t *testing.T) {
	c, _ := StartTestEnv(t)
	cases := []struct {
		name   string
		mutate func(*v1alpha1.Build)
		want   string
	}{
		{"http-url", func(x *v1alpha1.Build) { x.Spec.Source.Git.URL = "http://git.test/a.git" }, "spec.source.git.url"},
		{"ssh-url", func(x *v1alpha1.Build) { x.Spec.Source.Git.URL = "git@git.test:a.git" }, "spec.source.git.url"},
		{"url-with-credentials", func(x *v1alpha1.Build) { x.Spec.Source.Git.URL = "https://user:token@git.test/a.git" }, "spec.source.git.url"},
		{"url-with-space", func(x *v1alpha1.Build) { x.Spec.Source.Git.URL = "https://git.test/a b.git" }, "spec.source.git.url"},
		{"url-too-long", func(x *v1alpha1.Build) { x.Spec.Source.Git.URL = "https://git.test/" + strings.Repeat("a", 512) }, "spec.source.git.url"},
		{"empty-url", func(x *v1alpha1.Build) { x.Spec.Source.Git.URL = "" }, "spec.source.git.url"},
		{"revision-with-space", func(x *v1alpha1.Build) { x.Spec.Source.Git.Revision = "main branch" }, "spec.source.git.revision"},
		{"revision-too-long", func(x *v1alpha1.Build) { x.Spec.Source.Git.Revision = strings.Repeat("a", 256) }, "spec.source.git.revision"},
		{"empty-revision", func(x *v1alpha1.Build) { x.Spec.Source.Git.Revision = "" }, "spec.source.git.revision"},
		{"absolute-context", func(x *v1alpha1.Build) { x.Spec.ContextPath = "/etc" }, "the path must be relative and without .."},
		{"parent-context", func(x *v1alpha1.Build) { x.Spec.ContextPath = "../outside" }, "the path must be relative and without .."},
		{"inner-parent-context", func(x *v1alpha1.Build) { x.Spec.ContextPath = "a/../../b" }, "the path must be relative and without .."},
		{"absolute-dockerfile", func(x *v1alpha1.Build) { x.Spec.DockerfilePath = "/Dockerfile" }, "the path must be relative and without .."},
		{"parent-dockerfile", func(x *v1alpha1.Build) { x.Spec.DockerfilePath = "../Dockerfile" }, "the path must be relative and without .."},
		{"long-context", func(x *v1alpha1.Build) { x.Spec.ContextPath = strings.Repeat("a", 256) }, "spec.contextPath"},
		{"unknown-type", func(x *v1alpha1.Build) { x.Spec.Type = "nix" }, "Unsupported value"},
		{"unknown-size", func(x *v1alpha1.Build) { x.Spec.Size = "xl" }, "Unsupported value"},
		{"empty-image", func(x *v1alpha1.Build) { x.Spec.Output.Image = "" }, "spec.output.image"},
		{"image-without-registry", func(x *v1alpha1.Build) { x.Spec.Output.Image = "shop" }, "spec.output.image"},
		{"image-with-space", func(x *v1alpha1.Build) { x.Spec.Output.Image = "registry.test/a b" }, "spec.output.image"},
		{"no-push-secret", func(x *v1alpha1.Build) { x.Spec.Output.PushSecretRef = "" }, "spec.output.pushSecretRef"},
		{"no-subnet", func(x *v1alpha1.Build) { x.Spec.Network.SubnetRef = "" }, "spec.network.subnetRef"},
		{"bad-git-secret", func(x *v1alpha1.Build) { x.Spec.Source.Git.SecretRef = "Bad_Name" }, "spec.source.git.secretRef"},
		{"name-too-long", func(x *v1alpha1.Build) { x.Name = strings.Repeat("a", 53) }, "the name must have at most 52 characters"},
		{"name-uppercase", func(x *v1alpha1.Build) { x.Name = "Shop" }, "the name must be a DNS label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := validBuild("bad-" + tc.name)
			tc.mutate(x)
			createInvalid(t, c, x, tc.want)
		})
	}
}

func TestBuildSpecIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	build := validBuild("immutable")
	createValid(t, c, build)

	var fresh v1alpha1.Build
	get := func() *v1alpha1.Build {
		if err := c.Get(ctx, client.ObjectKeyFromObject(build), &fresh); err != nil {
			t.Fatal(err)
		}
		return fresh.DeepCopy()
	}
	revision := get()
	revision.Spec.Source.Git.Revision = "main"
	updateInvalid(t, c, revision, "a build is immutable")
	image := get()
	image.Spec.Output.Image = "registry.test/acme/shop:2"
	updateInvalid(t, c, image, "a build is immutable")
	cache := get()
	cache.Spec.Cache.Enabled = true
	updateInvalid(t, c, cache, "a build is immutable")
	timeout := get()
	timeout.Spec.Timeout = &metav1.Duration{Duration: time.Hour}
	updateInvalid(t, c, timeout, "a build is immutable")

	labeled := get()
	labeled.Labels = map[string]string{"app": "shop"}
	if err := c.Update(ctx, labeled); err != nil {
		t.Fatalf("metadata must stay editable: %v", err)
	}
}

func TestBuildStatus(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	build := validBuild("status")
	createValid(t, c, build)
	now := metav1.Now()
	build.Status = v1alpha1.BuildStatus{
		Phase:      "Succeeded",
		Reason:     "Complete",
		JobName:    "status",
		StartedAt:  &now,
		FinishedAt: &now,
		Image:      "registry.test/acme/shop:1",
	}
	if err := c.Status().Update(ctx, build); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	for _, phase := range []string{"Pending", "Running", "Failed"} {
		build.Status.Phase = phase
		if err := c.Status().Update(ctx, build); err != nil {
			t.Fatalf("phase %s: %v", phase, err)
		}
	}
	build.Status.Phase = "Paused"
	requireInvalid(t, c.Status().Update(ctx, build), "Unsupported value")
}
