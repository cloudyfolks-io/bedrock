package operator

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const metricsAddress = ":8080"

var (
	conditionDesc   = prometheus.NewDesc("bedrock_condition_status", "Status of a condition: 1 for True, 0 for False.", []string{"kind", "name", "type", "reason"}, nil)
	transitionDesc  = prometheus.NewDesc("bedrock_condition_transition_timestamp_seconds", "Time of the last change of a condition.", []string{"kind", "name", "type"}, nil)
	phaseDesc       = prometheus.NewDesc("bedrock_cluster_phase", "Current phase of the Cluster, 1 for the phase that is active.", []string{"phase"}, nil)
	phaseStartDesc  = prometheus.NewDesc("bedrock_cluster_upgrade_phase_start_timestamp_seconds", "Start time of the current upgrade phase.", []string{"phase"}, nil)
	stepFailedDesc  = prometheus.NewDesc("bedrock_nodeupgrade_step_failed", "1 when a NodeUpgrade step failed.", []string{"node", "step"}, nil)
	stepAttemptDesc = prometheus.NewDesc("bedrock_nodeupgrade_step_attempt", "Attempt of a NodeUpgrade step that has not succeeded.", []string{"node", "step"}, nil)
)

type sample struct {
	desc   *prometheus.Desc
	value  float64
	labels []string
}

func conditionSamples(kind, name string, conditions []metav1.Condition) []sample {
	samples := make([]sample, 0, 2*len(conditions))
	for _, condition := range conditions {
		value := 0.0
		if condition.Status == metav1.ConditionTrue {
			value = 1
		}
		samples = append(samples,
			sample{conditionDesc, value, []string{kind, name, condition.Type, condition.Reason}},
			sample{transitionDesc, float64(condition.LastTransitionTime.Unix()), []string{kind, name, condition.Type}},
		)
	}
	return samples
}

func clusterSamples(cluster v1alpha1.Cluster) []sample {
	conditions := make([]metav1.Condition, 0, len(cluster.Status.Conditions))
	for _, condition := range cluster.Status.Conditions {
		if condition.Type == v1alpha1.ConditionTenantLegsGuarded && len(cluster.Spec.TenantLegs) == 0 {
			continue
		}
		conditions = append(conditions, condition)
	}
	samples := conditionSamples("Cluster", cluster.Name, conditions)
	if cluster.Status.Phase == "" {
		return samples
	}
	samples = append(samples, sample{phaseDesc, 1, []string{cluster.Status.Phase}})
	if upgrade := cluster.Status.Upgrade; upgrade != nil {
		samples = append(samples, sample{phaseStartDesc, float64(upgrade.PhaseStartedAt.Unix()), []string{cluster.Status.Phase}})
	}
	return samples
}

func nodeUpgradeSamples(upgrade v1alpha1.NodeUpgrade) []sample {
	var samples []sample
	for _, step := range upgrade.Status.Steps {
		if step.State == v1alpha1.StepSucceeded {
			continue
		}
		labels := []string{upgrade.Spec.Node, step.Name}
		failed := 0.0
		if step.State == v1alpha1.StepFailed {
			failed = 1
		}
		samples = append(samples, sample{stepFailedDesc, failed, labels}, sample{stepAttemptDesc, float64(step.Attempt), labels})
	}
	return samples
}

type statusCollector struct {
	reader client.Reader
}

func (statusCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{conditionDesc, transitionDesc, phaseDesc, phaseStartDesc, stepFailedDesc, stepAttemptDesc} {
		ch <- desc
	}
}

func (c statusCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var samples []sample
	var clusters v1alpha1.ClusterList
	if c.reader.List(ctx, &clusters) == nil {
		for _, cluster := range clusters.Items {
			samples = append(samples, clusterSamples(cluster)...)
		}
	}
	var hosts v1alpha1.HostList
	if c.reader.List(ctx, &hosts) == nil {
		for _, host := range hosts.Items {
			samples = append(samples, conditionSamples("Host", host.Name, host.Status.Conditions)...)
		}
	}
	var upgrades v1alpha1.NodeUpgradeList
	if c.reader.List(ctx, &upgrades) == nil {
		for _, upgrade := range upgrades.Items {
			samples = append(samples, nodeUpgradeSamples(upgrade)...)
		}
	}
	for _, s := range samples {
		ch <- prometheus.MustNewConstMetric(s.desc, prometheus.GaugeValue, s.value, s.labels...)
	}
}

func registerStatusMetrics(reader client.Reader) error {
	err := ctrlmetrics.Registry.Register(statusCollector{reader: reader})
	if _, already := err.(prometheus.AlreadyRegisteredError); already {
		return nil
	}
	return err
}
