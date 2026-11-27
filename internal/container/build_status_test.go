package container

import (
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var decideNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func jobWith(conditions ...batchv1.JobCondition) *batchv1.Job {
	started := metav1.NewTime(decideNow.Add(-10 * time.Minute))
	return &batchv1.Job{Status: batchv1.JobStatus{StartTime: &started, Conditions: conditions}}
}

func jobCondition(kind batchv1.JobConditionType, reason string, at time.Time) batchv1.JobCondition {
	return batchv1.JobCondition{Type: kind, Status: corev1.ConditionTrue, Reason: reason, Message: reason + " message", LastTransitionTime: metav1.NewTime(at)}
}

func podAged(age time.Duration, mutate func(*corev1.Pod)) corev1.Pod {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", CreationTimestamp: metav1.NewTime(decideNow.Add(-age))}}
	mutate(&pod)
	return pod
}

func waiting(reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: "c", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: reason + " detail"}}}
}

func running() corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

func TestDecideBuild(t *testing.T) {
	missingFor := func(d time.Duration) *time.Time { since := decideNow.Add(-d); return &since }
	cases := []struct {
		name       string
		obs        BuildObservation
		phase      string
		reason     string
		terminal   bool
		hasStarted bool
	}{
		{"complete", BuildObservation{Job: jobWith(jobCondition(batchv1.JobComplete, "", decideNow))}, v1alpha1.BuildPhaseSucceeded, ReasonSucceeded, true, true},
		{"failed", BuildObservation{Job: jobWith(jobCondition(batchv1.JobFailed, "BackoffLimitExceeded", decideNow))}, v1alpha1.BuildPhaseFailed, ReasonFailed, true, true},
		{"deadline", BuildObservation{Job: jobWith(jobCondition(batchv1.JobFailed, "DeadlineExceeded", decideNow))}, v1alpha1.BuildPhaseFailed, ReasonTimeout, true, true},
		{"a condition that is not true counts for nothing", BuildObservation{Job: jobWith(batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionFalse})}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"job without a pod", BuildObservation{Job: jobWith()}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"pod with a running init container", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(time.Second, func(p *corev1.Pod) { p.Status.InitContainerStatuses = []corev1.ContainerStatus{running()} })}}, v1alpha1.BuildPhaseRunning, ReasonRunning, false, true},
		{"pod in phase running", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(time.Second, func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning })}}, v1alpha1.BuildPhaseRunning, ReasonRunning, false, true},
		{"pod that only waits to be created", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(time.Second, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{waiting("PodInitializing")}
		})}}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"image pull back-off before two minutes", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(119*time.Second, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{waiting("ImagePullBackOff")}
		})}}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"image pull back-off after two minutes", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(121*time.Second, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{waiting("ImagePullBackOff")}
		})}}, v1alpha1.BuildPhaseFailed, ReasonImagePullFailed, true, true},
		{"err image pull", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(3*time.Minute, func(p *corev1.Pod) { p.Status.ContainerStatuses = []corev1.ContainerStatus{waiting("ErrImagePull")} })}}, v1alpha1.BuildPhaseFailed, ReasonImagePullFailed, true, true},
		{"invalid image name", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(3*time.Minute, func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{waiting("InvalidImageName")}
		})}}, v1alpha1.BuildPhaseFailed, ReasonImagePullFailed, true, true},
		{"create container config error", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(3*time.Minute, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{waiting("CreateContainerConfigError")}
		})}}, v1alpha1.BuildPhaseFailed, ReasonContainerConfigError, true, true},
		{"create container error", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(3*time.Minute, func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{waiting("CreateContainerError")}
		})}}, v1alpha1.BuildPhaseFailed, ReasonContainerConfigError, true, true},
		{"an old pod that waits for another reason", BuildObservation{Job: jobWith(), Pods: []corev1.Pod{podAged(time.Hour, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{waiting("PodInitializing")}
		})}}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"complete beats a stuck pod", BuildObservation{Job: jobWith(jobCondition(batchv1.JobComplete, "", decideNow)), Pods: []corev1.Pod{podAged(time.Hour, func(p *corev1.Pod) { p.Status.ContainerStatuses = []corev1.ContainerStatus{waiting("ErrImagePull")} })}}, v1alpha1.BuildPhaseSucceeded, ReasonSucceeded, true, true},
		{"no job and nothing known yet", BuildObservation{}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"job gone for 59 seconds", BuildObservation{JobMissingSince: missingFor(59 * time.Second)}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"job gone for exactly one minute", BuildObservation{JobMissingSince: missingFor(time.Minute)}, v1alpha1.BuildPhasePending, ReasonStarting, false, false},
		{"job gone for 61 seconds", BuildObservation{JobMissingSince: missingFor(61 * time.Second)}, v1alpha1.BuildPhaseFailed, ReasonJobMissing, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.obs.Now = decideNow
			got := DecideBuild(tc.obs)
			if got.Phase != tc.phase || got.Reason != tc.reason {
				t.Fatalf("decision %s %s, want %s %s", got.Phase, got.Reason, tc.phase, tc.reason)
			}
			if got.Terminal() != tc.terminal {
				t.Fatalf("terminal %v, want %v", got.Terminal(), tc.terminal)
			}
			if (got.StartedAt != nil) != tc.hasStarted {
				t.Fatalf("startedAt %v, want set %v", got.StartedAt, tc.hasStarted)
			}
			if got.Terminal() && got.FinishedAt == nil {
				t.Fatal("a terminal decision needs a finish time")
			}
			if !got.Terminal() && got.FinishedAt != nil {
				t.Fatal("a running decision has no finish time")
			}
			if got.Message == "" {
				t.Fatal("every decision explains itself")
			}
		})
	}
}

func TestDecideBuildTimesComeFromTheJob(t *testing.T) {
	completed := decideNow.Add(-time.Minute)
	job := jobWith(jobCondition(batchv1.JobComplete, "", decideNow))
	finish := metav1.NewTime(completed)
	job.Status.CompletionTime = &finish
	got := DecideBuild(BuildObservation{Job: job, Now: decideNow})
	if !got.FinishedAt.Equal(completed) || !got.StartedAt.Equal(decideNow.Add(-10*time.Minute)) {
		t.Fatalf("times %v %v", got.StartedAt, got.FinishedAt)
	}
	failedAt := decideNow.Add(-2 * time.Minute)
	got = DecideBuild(BuildObservation{Job: jobWith(jobCondition(batchv1.JobFailed, "DeadlineExceeded", failedAt)), Now: decideNow})
	if !got.FinishedAt.Equal(failedAt) {
		t.Fatalf("failed at %v", got.FinishedAt)
	}
}

func TestBuildStatusOfKeepsTimesAndImage(t *testing.T) {
	started := metav1.NewTime(decideNow.Add(-time.Hour))
	build := v1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Generation: 3},
		Spec:       v1alpha1.BuildSpec{Output: v1alpha1.BuildOutput{Image: "registry.test/a/b:1"}},
		Status:     v1alpha1.BuildStatus{StartedAt: &started},
	}
	finished := decideNow.Add(-time.Minute)
	status := BuildStatusOf(build, nil, BuildDecision{Phase: v1alpha1.BuildPhaseSucceeded, Reason: ReasonSucceeded, Message: "done", StartedAt: &finished, FinishedAt: &finished}, "b", true, decideNow)
	if !status.StartedAt.Equal(&started) {
		t.Fatalf("startedAt changed: %v", status.StartedAt)
	}
	if status.FinishedAt == nil || !status.FinishedAt.Time.Equal(finished) || status.Image != "registry.test/a/b:1" || status.JobName != "b" || status.ObservedGeneration != 3 {
		t.Fatalf("status %+v", status)
	}
	if len(status.Conditions) != 2 || status.Conditions[0].Type != v1alpha1.ConditionReady || status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("conditions %+v", status.Conditions)
	}
}

func TestBuildStatusOfAGateIsPendingWithoutAJobCondition(t *testing.T) {
	build := v1alpha1.Build{ObjectMeta: metav1.ObjectMeta{Name: "b"}}
	status := BuildStatusOf(build, gateOf(ReasonSecretNotFound, "no secret"), BuildDecision{}, "", false, decideNow)
	if status.Phase != v1alpha1.BuildPhasePending || status.Reason != ReasonSecretNotFound || status.JobName != "" || status.Image != "" {
		t.Fatalf("status %+v", status)
	}
	if len(status.Conditions) != 1 || status.Conditions[0].Reason != ReasonSecretNotFound || status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("conditions %+v", status.Conditions)
	}
}

func TestBuildStatusOfKeepsTheTransitionTimeOfAnUnchangedCondition(t *testing.T) {
	before := metav1.NewTime(decideNow.Add(-time.Hour))
	build := v1alpha1.Build{Status: v1alpha1.BuildStatus{CommonStatus: v1alpha1.CommonStatus{Conditions: []metav1.Condition{
		{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, LastTransitionTime: before},
		{Type: ConditionJobExists, Status: metav1.ConditionFalse, LastTransitionTime: before},
	}}}}
	status := BuildStatusOf(build, nil, BuildDecision{Phase: v1alpha1.BuildPhasePending, Reason: ReasonStarting, Message: "wait"}, "b", false, decideNow)
	for _, condition := range status.Conditions {
		if !condition.LastTransitionTime.Equal(&before) {
			t.Errorf("condition %s moved to %v", condition.Type, condition.LastTransitionTime)
		}
	}
	if since := JobMissingSince(v1alpha1.Build{Status: status}); since == nil || !since.Equal(before.Time) {
		t.Fatalf("missing since %v", since)
	}
	recovered := BuildStatusOf(v1alpha1.Build{Status: status}, nil, BuildDecision{Phase: v1alpha1.BuildPhaseRunning, Reason: ReasonRunning, Message: "go"}, "b", true, decideNow)
	if JobMissingSince(v1alpha1.Build{Status: recovered}) != nil {
		t.Fatal("a job that exists again is not missing")
	}
}

func TestBuildReasonsListTheFailureReasons(t *testing.T) {
	for _, want := range []string{ReasonCloneFailed, ReasonBuildFailed, ReasonPushFailed, ReasonTimeout, ReasonFailed} {
		if !slices.Contains(BuildReasons(), want) {
			t.Errorf("BuildReasons lacks %s", want)
		}
	}
}

func TestBuildReasonsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range BuildReasons() {
		if seen[reason] {
			t.Errorf("the reason %s is listed twice", reason)
		}
		seen[reason] = true
	}
}

func failedPod(inits []corev1.ContainerStatus, mains []corev1.ContainerStatus) corev1.Pod {
	return podAged(time.Minute, func(p *corev1.Pod) {
		p.Status.InitContainerStatuses = inits
		p.Status.ContainerStatuses = mains
	})
}

func terminated(name string, code int32, message string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Message: message}}}
}

func TestDecideBuildSeparatesTheFailures(t *testing.T) {
	failed := jobWith(jobCondition(batchv1.JobFailed, "BackoffLimitExceeded", decideNow))
	cases := []struct {
		name   string
		pods   []corev1.Pod
		reason string
		inMsg  string
	}{
		{"clone failed", []corev1.Pod{failedPod([]corev1.ContainerStatus{terminated("git-clone", 128, "fatal: repository not found")}, nil)}, ReasonCloneFailed, "fatal: repository not found"},
		{"clone failed without a message", []corev1.Pod{failedPod([]corev1.ContainerStatus{terminated("git-clone", 128, "")}, nil)}, ReasonCloneFailed, "git-clone"},
		{"dockerfile build failed", []corev1.Pod{failedPod([]corev1.ContainerStatus{terminated("git-clone", 0, "")}, []corev1.ContainerStatus{terminated("build", 1, "error: failed to solve: process \"/bin/sh -c false\" did not complete successfully")})}, ReasonBuildFailed, "failed to solve"},
		{"dockerfile push failed", []corev1.Pod{failedPod([]corev1.ContainerStatus{terminated("git-clone", 0, "")}, []corev1.ContainerStatus{terminated("build", 1, "error: failed to solve: failed to push registry.test/a/b:1: unauthorized")})}, ReasonPushFailed, "failed to push"},
		{"buildpacks export failed", []corev1.Pod{failedPod([]corev1.ContainerStatus{terminated("git-clone", 0, ""), terminated("analyzer", 0, ""), terminated("detector", 0, ""), terminated("restorer", 0, ""), terminated("builder", 0, "")}, []corev1.ContainerStatus{terminated("exporter", 1, "ERROR: failed to export: failed to write image to the following tags")})}, ReasonPushFailed, "failed to export"},
		{"buildpacks phase failed", []corev1.Pod{failedPod([]corev1.ContainerStatus{terminated("git-clone", 0, ""), terminated("analyzer", 0, ""), terminated("detector", 20, "ERROR: No buildpack groups passed detection.")}, nil)}, ReasonBuildFailed, "No buildpack groups"},
		{"no pod", nil, ReasonFailed, "backoff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideBuild(BuildObservation{Job: failed, Pods: tc.pods, Now: decideNow})
			if got.Phase != v1alpha1.BuildPhaseFailed || got.Reason != tc.reason {
				t.Fatalf("decision %s %s, want Failed %s", got.Phase, got.Reason, tc.reason)
			}
			if !strings.Contains(strings.ToLower(got.Message), strings.ToLower(tc.inMsg)) {
				t.Fatalf("message %q lacks %q", got.Message, tc.inMsg)
			}
		})
	}
}

func TestDecideBuildKeepsTheTimeoutAboveTheContainerFailures(t *testing.T) {
	job := jobWith(jobCondition(batchv1.JobFailed, "DeadlineExceeded", decideNow))
	pod := failedPod([]corev1.ContainerStatus{terminated("git-clone", 0, "")}, []corev1.ContainerStatus{terminated("build", 137, "killed")})
	if got := DecideBuild(BuildObservation{Job: job, Pods: []corev1.Pod{pod}, Now: decideNow}); got.Reason != ReasonTimeout {
		t.Fatalf("reason %s", got.Reason)
	}
}

func TestDecideBuildCutsALongTerminationMessage(t *testing.T) {
	failed := jobWith(jobCondition(batchv1.JobFailed, "BackoffLimitExceeded", decideNow))
	pod := failedPod([]corev1.ContainerStatus{terminated("git-clone", 128, strings.Repeat("x", 5000))}, nil)
	if got := DecideBuild(BuildObservation{Job: failed, Pods: []corev1.Pod{pod}, Now: decideNow}); len(got.Message) > 700 {
		t.Fatalf("message of %d bytes", len(got.Message))
	}
}
