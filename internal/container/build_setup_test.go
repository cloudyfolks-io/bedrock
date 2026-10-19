package container

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func buildPhase(t *testing.T, c client.Client, namespace, name string) (string, string) {
	t.Helper()
	var build v1alpha1.Build
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &build); err != nil {
		return "", ""
	}
	return build.Status.Phase, build.Status.Reason
}

func TestSetupRunsBuildsAndFollowsTheirJobs(t *testing.T) {
	c, cfg := startFreshEnv(t)
	startManager(t, cfg)
	ns := uniqueNamespace(t, c)
	ensureNetwork(t, c)
	ensureDefaultGroup(t, c, ns)
	ensureRuntimeClass(t, c, "kata")
	createBuildSecrets(t, c, ns)
	setKataAddon(t, c, metav1.ConditionTrue)
	createBuild(t, c, ns, "shop", noBuildMutation)
	eventually(t, "the subnet gate", func() bool {
		phase, reason := buildPhase(t, c, ns, "shop")
		return phase == v1alpha1.BuildPhasePending && reason == ReasonSubnetNotFound
	})

	createSubnet(t, c, ns, "default", metav1.ConditionTrue)
	eventually(t, "the job", func() bool {
		var job batchv1.Job
		return c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "shop"}, &job) == nil
	})
	eventually(t, "the starting build", func() bool {
		phase, reason := buildPhase(t, c, ns, "shop")
		return phase == v1alpha1.BuildPhasePending && reason == ReasonStarting
	})

	completeJob(t, c, ns, "shop")
	eventually(t, "the finished build", func() bool {
		phase, _ := buildPhase(t, c, ns, "shop")
		return phase == v1alpha1.BuildPhaseSucceeded
	})
}
