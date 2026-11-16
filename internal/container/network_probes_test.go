package container

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestRenderNetworkProbesUsesTheirHandlersAndRequiresManagementNetworking(t *testing.T) {
	for _, probe := range []*v1alpha1.ContainerProbe{
		{HTTPGet: &v1alpha1.ContainerHTTPGet{Path: "/ready", Port: 8080}},
		{TCPSocket: &v1alpha1.ContainerTCPSocket{Port: 8081}},
		{GRPC: &v1alpha1.ContainerGRPC{Port: 9090, Service: "health"}},
	} {
		in := withSpec(func(s *v1alpha1.ContainerSpec) {
			s.Probes = &v1alpha1.ContainerProbes{Readiness: probe}
		})
		if _, err := RenderDeployment(in); err == nil {
			t.Fatal("a network probe was rendered on the isolated one-interface path")
		}
		in.NIC.Class = network.NetworkClassWorkload
		deployment, _ := mustDeployment(t, in)
		got := appContainer(t, deployment).ReadinessProbe
		if got == nil || got.Exec != nil {
			t.Fatalf("probe %+v", got)
		}
		switch {
		case probe.HTTPGet != nil:
			if got.HTTPGet == nil || got.HTTPGet.Port.IntVal != 8080 || got.HTTPGet.Path != "/ready" {
				t.Fatalf("http probe %+v", got)
			}
		case probe.TCPSocket != nil:
			if got.TCPSocket == nil || got.TCPSocket.Port.IntVal != 8081 {
				t.Fatalf("tcp probe %+v", got)
			}
		case probe.GRPC != nil:
			if got.GRPC == nil || got.GRPC.Port != 9090 || got.GRPC.Service == nil || *got.GRPC.Service != "health" {
				t.Fatalf("grpc probe %+v", got)
			}
		}
	}
}

func TestContainerCRDValidatesNetworkProbeHandlersAndPorts(t *testing.T) {
	c, _ := startEnv(t)
	namespace := uniqueNamespace(t, c)
	for i, probe := range []*v1alpha1.ContainerProbe{
		{},
		{Exec: &v1alpha1.ContainerExec{Command: []string{"true"}}, HTTPGet: &v1alpha1.ContainerHTTPGet{Port: 8080}},
		{HTTPGet: &v1alpha1.ContainerHTTPGet{Port: 0}},
		{TCPSocket: &v1alpha1.ContainerTCPSocket{Port: -1}},
		{GRPC: &v1alpha1.ContainerGRPC{Port: 65536}},
	} {
		container := fixtureContainer(namespace, fmt.Sprintf("bad-probe-%d", i))
		container.Spec.Probes = &v1alpha1.ContainerProbes{Readiness: probe}
		if err := c.Create(context.Background(), &container); !apierrors.IsInvalid(err) {
			t.Fatalf("invalid probe %d: %v", i, err)
		}
	}
	for i, probe := range []*v1alpha1.ContainerProbe{
		{Exec: &v1alpha1.ContainerExec{Command: []string{"true"}}},
		{HTTPGet: &v1alpha1.ContainerHTTPGet{Port: 80}},
		{TCPSocket: &v1alpha1.ContainerTCPSocket{Port: 65535}},
		{GRPC: &v1alpha1.ContainerGRPC{Port: 9090}},
	} {
		container := fixtureContainer(namespace, fmt.Sprintf("good-probe-%d", i))
		container.Spec.Probes = &v1alpha1.ContainerProbes{Readiness: probe}
		if err := c.Create(context.Background(), &container); err != nil {
			t.Fatalf("valid probe %d: %v", i, err)
		}
	}
}
