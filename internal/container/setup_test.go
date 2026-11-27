package container

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/testenv"
)

func startFreshEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	env := &envtest.Environment{CRDDirectoryPaths: testenv.CRDPaths(), ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	must(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	must(t, err)
	return c, cfg
}

func startManager(t *testing.T, cfg *rest.Config) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     testScheme(t),
		Metrics:    server.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	must(t, err)
	must(t, SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func containerReady(t *testing.T, c client.Client, namespace, name string) (metav1.ConditionStatus, string) {
	t.Helper()
	var container v1alpha1.Container
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &container); err != nil {
		return "", ""
	}
	if cond := readyOf(container); cond != nil {
		return cond.Status, cond.Reason
	}
	return "", ""
}

func TestSetupReconcilesContainersAndFollowsTheirDependencies(t *testing.T) {
	c, cfg := startFreshEnv(t)
	startManager(t, cfg)
	ns := uniqueNamespace(t, c)
	ensureNetwork(t, c)
	ensureDefaultGroup(t, c, ns)
	ensureRuntimeClass(t, c, "kata")
	createContainer(t, c, ns, "web", func(s *v1alpha1.ContainerSpec) { s.Network.SecurityGroups = []string{"web"} })
	eventually(t, "the subnet gate", func() bool {
		status, reason := containerReady(t, c, ns, "web")
		return status == metav1.ConditionFalse && reason == ReasonSubnetNotFound
	})

	createSubnet(t, c, ns, "default", metav1.ConditionTrue)
	eventually(t, "the security group gate", func() bool {
		_, reason := containerReady(t, c, ns, "web")
		return reason == ReasonSecurityGroupNotReady
	})

	createSecurityGroup(t, c, ns, "web", metav1.ConditionTrue)
	eventually(t, "the deployment", func() bool {
		_, reason := containerReady(t, c, ns, "web")
		return reason == ReasonDeploying
	})
	getDeployment(t, c, ns, "web")

	markDeploymentRunning(t, c, ns, "web", 1)
	eventually(t, "the running status after a deployment change", func() bool {
		status, reason := containerReady(t, c, ns, "web")
		return status == metav1.ConditionTrue && reason == ReasonRunning
	})

	setRuntimeSetting(t, c, "absent-class")
	eventually(t, "the runtime gate after a Setting change", func() bool {
		_, reason := containerReady(t, c, ns, "web")
		return reason == ReasonRuntimeUnavailable
	})
}

func TestSetupFollowsThePodsOfAContainer(t *testing.T) {
	c, cfg := startFreshEnv(t)
	startManager(t, cfg)
	ns := uniqueNamespace(t, c)
	seedPlatformIn(t, c, ns)
	createContainer(t, c, ns, "web", noMutation)
	eventually(t, "the first status", func() bool {
		_, reason := containerReady(t, c, ns, "web")
		return reason == ReasonDeploying
	})
	pod := createPod(t, c, ns, "web-1-a", "web")
	updateStatus(t, c, &pod, func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "denied"}}}}
	})
	eventually(t, "the image pull failure", func() bool {
		_, reason := containerReady(t, c, ns, "web")
		return reason == ReasonImagePullFailed
	})
}
