package images

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type World struct {
	PlatformHost string
	Cron         *unstructured.Unstructured
	DataVolume   *unstructured.Unstructured
	ImportClaim  *corev1.PersistentVolumeClaim
	SourceClaim  *corev1.PersistentVolumeClaim
	Snapshots    []unstructured.Unstructured
	DataSources  []unstructured.Unstructured
	Prev         v1alpha1.ImageStatus
}

type Plan struct {
	Apply   []*unstructured.Unstructured
	Delete  []*unstructured.Unstructured
	Result  Result
	Requeue time.Duration
}

func Decide(s Subject, w World, now time.Time) Plan {
	plan := decideVolume(s, w)
	if UsesCron(s.Spec) {
		plan = decideCron(s, w, now)
	}
	plan.Result = keepPrevious(plan.Result, w.Prev)
	if plan.Requeue == 0 {
		plan.Requeue = requeueOf(plan.Result.Phase)
	}
	return plan
}

func keepPrevious(r Result, prev v1alpha1.ImageStatus) Result {
	if r.DataSource == "" {
		r.DataSource, r.Digest, r.SizeBytes = prev.DataSource, prev.Digest, prev.SizeBytes
	}
	if r.Versions == nil {
		r.Versions = prev.Versions
	}
	return r
}

func requeueOf(phase string) time.Duration {
	switch phase {
	case v1alpha1.ImagePhasePending, v1alpha1.ImagePhaseImporting:
		return 10 * time.Second
	case v1alpha1.ImagePhaseWaitingForUpload:
		return 30 * time.Second
	case v1alpha1.ImagePhaseFailed:
		return time.Minute
	default:
		return 0
	}
}

func decideCron(Subject, World, time.Time) Plan {
	return Plan{}
}
