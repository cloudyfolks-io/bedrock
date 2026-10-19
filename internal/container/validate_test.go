package container

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestValidateContainerAcceptsAGoodSpec(t *testing.T) {
	c := baseInput().Container
	c.Spec.Ports = []v1alpha1.ContainerPort{{Name: "http", Port: 80}, {Name: "metrics", Port: 9090}}
	c.Spec.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
	c.Spec.Probes = &v1alpha1.ContainerProbes{Liveness: &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"true"}}}}
	if issues := ValidateContainer(c); len(issues) != 0 {
		t.Fatalf("issues %+v", issues)
	}
}

func TestValidateContainerFindsWhatCELCannotCheck(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.ContainerSpec)
		field  string
		text   string
	}{
		{"image-space", func(s *v1alpha1.ContainerSpec) { s.Image = "registry.test/web 1" }, "spec.image", "no spaces"},
		{"image-flag", func(s *v1alpha1.ContainerSpec) { s.Image = "--privileged" }, "spec.image", "no spaces"},
		{"port-name-twice", func(s *v1alpha1.ContainerSpec) {
			s.Ports = []v1alpha1.ContainerPort{{Name: "web", Port: 80}, {Name: "web", Port: 81}}
		}, "spec.ports[1].name", "used twice"},
		{"zero-cpu", func(s *v1alpha1.ContainerSpec) { s.Resources.Requests.CPU = ptr.To(resource.MustParse("0")) }, "spec.resources.requests.cpu", "above zero"},
		{"zero-memory", func(s *v1alpha1.ContainerSpec) { s.Resources.Limits.Memory = ptr.To(resource.MustParse("0")) }, "spec.resources.limits.memory", "above zero"},
		{"liveness-threshold", func(s *v1alpha1.ContainerSpec) {
			s.Probes = &v1alpha1.ContainerProbes{Liveness: &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"x"}}, SuccessThreshold: ptr.To(int32(2))}}
		}, "spec.probes.liveness.successThreshold", "must be 1"},
		{"empty-command-word", func(s *v1alpha1.ContainerSpec) {
			s.Probes = &v1alpha1.ContainerProbes{Readiness: &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{""}}}}
		}, "spec.probes.readiness.exec.command", "must not be empty"},
		{"mount-dotdot", func(s *v1alpha1.ContainerSpec) {
			s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data/../etc"}}
		}, "spec.volumes[0].mountPath", "clean absolute path"},
		{"mount-proc", func(s *v1alpha1.ContainerSpec) {
			s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/proc/x"}}
		}, "spec.volumes[0].mountPath", "inside /proc"},
		{"mount-dev", func(s *v1alpha1.ContainerSpec) {
			s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/dev"}}
		}, "spec.volumes[0].mountPath", "inside /proc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseInput().Container
			tc.mutate(&c.Spec)
			issues := ValidateContainer(c)
			if len(issues) != 1 || issues[0].Field != tc.field || !strings.Contains(issues[0].Message, tc.text) {
				t.Fatalf("issues %+v, want %s %q", issues, tc.field, tc.text)
			}
		})
	}
}

func TestIssueMessageJoinsFieldAndText(t *testing.T) {
	got := IssueMessage([]Issue{{Field: "a", Message: "x"}, {Field: "b", Message: "y"}})
	if got != "a: x; b: y" {
		t.Fatalf("message %q", got)
	}
}
