package release

import (
	"slices"
	"testing"
)

func TestContainerdName(t *testing.T) {
	digest := "sha256:24841fe2de7304c149343d877d2923b4c8800a38ba015dea9174c23b20e344a0"
	cases := map[string]string{
		"docker.io/traefik:v3.7.13":   "docker.io/library/traefik:v3.7.13",
		"docker.io/traefik@" + digest: "docker.io/library/traefik@" + digest,
		"traefik:v3.7.13":             "docker.io/library/traefik:v3.7.13",
		"docker.io/rook/ceph:v1.20.7": "docker.io/rook/ceph:v1.20.7",
		"quay.io/ceph/ceph:v20.2.4":   "quay.io/ceph/ceph:v20.2.4",
		"localhost:5000/bedrock:dev":  "localhost:5000/bedrock:dev",
	}
	for ref, want := range cases {
		got, err := containerdName(ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got != want {
			t.Fatalf("%s: got %s, want %s", ref, got, want)
		}
	}
}

func TestContainerdNames(t *testing.T) {
	digest := "sha256:24841fe2de7304c149343d877d2923b4c8800a38ba015dea9174c23b20e344a0"
	cases := map[string][]string{
		"quay.io/kubevirt/cdi-uploadproxy:v1.66.1":           {"quay.io/kubevirt/cdi-uploadproxy:v1.66.1"},
		"quay.io/kubevirt/cdi-uploadproxy:v1.66.1@" + digest: {"quay.io/kubevirt/cdi-uploadproxy:v1.66.1", "quay.io/kubevirt/cdi-uploadproxy@" + digest},
		"docker.io/traefik:v3.7.13@" + digest:                {"docker.io/library/traefik:v3.7.13", "docker.io/library/traefik@" + digest},
		"quay.io/kubevirt/cdi-uploadproxy@" + digest:         {"quay.io/kubevirt/cdi-uploadproxy@" + digest},
		"localhost:5000/bedrock@" + digest:                   {"localhost:5000/bedrock@" + digest},
	}
	for ref, want := range cases {
		got, err := ContainerdNames(ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: got %v, want %v", ref, got, want)
		}
	}
}
