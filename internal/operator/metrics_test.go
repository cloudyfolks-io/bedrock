package operator

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func labelsOf(samples []sample, desc string) [][]string {
	var all [][]string
	for _, s := range samples {
		if strings.Contains(s.desc.String(), `"`+desc+`"`) {
			all = append(all, s.labels)
		}
	}
	return all
}

func TestClusterSamplesExportConditionsPhaseAndPhaseStart(t *testing.T) {
	started := metav1.NewTime(time.Unix(1700000000, 0))
	cluster := v1alpha1.Cluster{}
	cluster.Name = "cluster"
	cluster.Status.Phase = v1alpha1.PhaseWorkers
	cluster.Status.Upgrade = &v1alpha1.UpgradeStatus{PhaseStartedAt: started}
	cluster.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionNodePortsScoped, Status: metav1.ConditionFalse, Reason: "Open", LastTransitionTime: started}}
	samples := clusterSamples(cluster)
	if got := labelsOf(samples, "bedrock_condition_status"); len(got) != 1 || got[0][2] != "NodePortsScoped" || got[0][3] != "Open" {
		t.Fatalf("conditions %v", got)
	}
	if samples[0].value != 0 {
		t.Errorf("False condition value %v", samples[0].value)
	}
	if got := labelsOf(samples, "bedrock_cluster_phase"); len(got) != 1 || got[0][0] != "Workers" {
		t.Errorf("phase %v", got)
	}
	if got := labelsOf(samples, "bedrock_cluster_upgrade_phase_start_timestamp_seconds"); len(got) != 1 {
		t.Errorf("phase start %v", got)
	}
}

func TestClusterSamplesSkipTenantLegsGuardedWithoutTenantLegs(t *testing.T) {
	cluster := v1alpha1.Cluster{}
	cluster.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionTenantLegsGuarded, Status: metav1.ConditionFalse, Reason: "NoTenantLegs"}}
	if got := labelsOf(clusterSamples(cluster), "bedrock_condition_status"); len(got) != 0 {
		t.Fatalf("exported %v", got)
	}
	cluster.Spec.TenantLegs = []v1alpha1.TenantLegSpec{{Interface: "eno1"}}
	if got := labelsOf(clusterSamples(cluster), "bedrock_condition_status"); len(got) != 1 {
		t.Fatalf("exported %v", got)
	}
}

func TestNodeUpgradeSamplesSkipSucceededSteps(t *testing.T) {
	upgrade := v1alpha1.NodeUpgrade{}
	upgrade.Spec.Node = "host-01"
	upgrade.Status.Steps = []v1alpha1.NodeUpgradeStepStatus{
		{Name: v1alpha1.StepBackup, State: v1alpha1.StepSucceeded, Attempt: 1},
		{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 3},
	}
	samples := nodeUpgradeSamples(upgrade)
	if len(samples) != 2 || samples[0].value != 1 || samples[1].value != 3 || samples[0].labels[1] != "K0sUpdate" {
		t.Fatalf("samples %+v", samples)
	}
}
