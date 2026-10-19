package container

import (
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	stuckPodGrace   = 2 * time.Minute
	jobMissingGrace = time.Minute
	maxExitMessage  = 512

	ConditionJobExists       = "JobExists"
	ConditionRegistryMirrors = "RegistryMirrors"
)

var pushFailureMarkers = []string{"failed to push", "failed to export", "failed to write image"}

type BuildObservation struct {
	Job             *batchv1.Job
	Pods            []corev1.Pod
	Now             time.Time
	JobMissingSince *time.Time
}

type BuildDecision struct {
	Phase      string
	Reason     string
	Message    string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

func IsTerminalBuildPhase(phase string) bool {
	return phase == v1alpha1.BuildPhaseSucceeded || phase == v1alpha1.BuildPhaseFailed
}

func (d BuildDecision) Terminal() bool {
	return IsTerminalBuildPhase(d.Phase)
}

func DecideBuild(obs BuildObservation) BuildDecision {
	if obs.Job == nil {
		return decideMissingJob(obs)
	}
	started := timeOf(obs.Job.Status.StartTime)
	for _, condition := range obs.Job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			return BuildDecision{Phase: v1alpha1.BuildPhaseSucceeded, Reason: ReasonSucceeded, Message: "the image is pushed", StartedAt: started, FinishedAt: firstTime(obs.Job.Status.CompletionTime, condition.LastTransitionTime)}
		case batchv1.JobFailed:
			reason, message := failureOf(condition, obs.Pods)
			return BuildDecision{Phase: v1alpha1.BuildPhaseFailed, Reason: reason, Message: message, StartedAt: started, FinishedAt: firstTime(nil, condition.LastTransitionTime)}
		}
	}
	if reason, message, stuck := stuckPod(obs.Pods, obs.Now); stuck {
		return BuildDecision{Phase: v1alpha1.BuildPhaseFailed, Reason: reason, Message: message, StartedAt: started, FinishedAt: &obs.Now}
	}
	if anyPodStarted(obs.Pods) {
		return BuildDecision{Phase: v1alpha1.BuildPhaseRunning, Reason: ReasonRunning, Message: "the build is running", StartedAt: started}
	}
	return BuildDecision{Phase: v1alpha1.BuildPhasePending, Reason: ReasonStarting, Message: "the build waits for its pod"}
}

func decideMissingJob(obs BuildObservation) BuildDecision {
	if obs.JobMissingSince != nil && obs.Now.Sub(*obs.JobMissingSince) > jobMissingGrace {
		return BuildDecision{Phase: v1alpha1.BuildPhaseFailed, Reason: ReasonJobMissing, Message: "the job of the build is gone before the build ended", FinishedAt: &obs.Now}
	}
	return BuildDecision{Phase: v1alpha1.BuildPhasePending, Reason: ReasonStarting, Message: "the build waits for its job"}
}

func failureOf(condition batchv1.JobCondition, pods []corev1.Pod) (string, string) {
	if condition.Reason == batchv1.JobReasonDeadlineExceeded {
		return ReasonTimeout, "the build ran longer than its time limit"
	}
	for _, pod := range pods {
		if reason, message, found := podFailure(pod); found {
			return reason, message
		}
	}
	message := condition.Message
	if message == "" {
		message = "the build failed"
	}
	return ReasonFailed, message
}

func podFailure(pod corev1.Pod) (string, string, bool) {
	if status, found := containerStatus(pod.Status.InitContainerStatuses, gitCloneContainerName); found && failedExit(status) {
		return ReasonCloneFailed, exitMessage(status), true
	}
	for _, status := range pod.Status.ContainerStatuses {
		if failedExit(status) {
			return mainFailureReason(status), exitMessage(status), true
		}
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if failedExit(status) {
			return ReasonBuildFailed, exitMessage(status), true
		}
	}
	return "", "", false
}

func failedExit(status corev1.ContainerStatus) bool {
	return status.State.Terminated != nil && status.State.Terminated.ExitCode != 0
}

func mainFailureReason(status corev1.ContainerStatus) string {
	message := strings.ToLower(status.State.Terminated.Message)
	for _, marker := range pushFailureMarkers {
		if strings.Contains(message, marker) {
			return ReasonPushFailed
		}
	}
	return ReasonBuildFailed
}

func exitMessage(status corev1.ContainerStatus) string {
	terminated := status.State.Terminated
	message := strings.TrimSpace(terminated.Message)
	if len(message) > maxExitMessage {
		message = "..." + message[len(message)-maxExitMessage:]
	}
	if message == "" {
		return fmt.Sprintf("container %s exited with code %d", status.Name, terminated.ExitCode)
	}
	return fmt.Sprintf("container %s exited with code %d: %s", status.Name, terminated.ExitCode, message)
}

func stuckPod(pods []corev1.Pod, now time.Time) (string, string, bool) {
	for _, pod := range pods {
		if now.Sub(pod.CreationTimestamp.Time) <= stuckPodGrace {
			continue
		}
		for _, status := range allContainerStatuses(pod) {
			if status.State.Waiting == nil {
				continue
			}
			switch status.State.Waiting.Reason {
			case waitingImagePullBackOff, waitingErrImagePull, waitingInvalidImageName:
				return ReasonImagePullFailed, waitingMessage(status), true
			case waitingConfigError, waitingCreateContainerError:
				return ReasonContainerConfigError, waitingMessage(status), true
			}
		}
	}
	return "", "", false
}

func waitingMessage(status corev1.ContainerStatus) string {
	return fmt.Sprintf("container %s: %s", status.Name, status.State.Waiting.Message)
}

func allContainerStatuses(pod corev1.Pod) []corev1.ContainerStatus {
	return append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
}

func anyPodStarted(pods []corev1.Pod) bool {
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning {
			return true
		}
		for _, status := range allContainerStatuses(pod) {
			if status.State.Running != nil || (status.State.Terminated != nil && status.State.Terminated.ExitCode == 0) {
				return true
			}
		}
	}
	return false
}

func timeOf(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := t.Time
	return &value
}

func firstTime(preferred *metav1.Time, fallback metav1.Time) *time.Time {
	if preferred != nil {
		return timeOf(preferred)
	}
	value := fallback.Time
	return &value
}

func buildReadyCondition(decision BuildDecision, gate *Gate) metav1.Condition {
	if gate != nil {
		return metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: gate.Reason, Message: gate.Message}
	}
	ready := metav1.ConditionFalse
	if decision.Phase == v1alpha1.BuildPhaseSucceeded {
		ready = metav1.ConditionTrue
	}
	return metav1.Condition{Type: v1alpha1.ConditionReady, Status: ready, Reason: decision.Reason, Message: decision.Message}
}

func conditionsOf(decision BuildDecision, gate *Gate, jobName string, jobExists bool) []metav1.Condition {
	conditions := []metav1.Condition{buildReadyCondition(decision, gate)}
	if jobName != "" {
		conditions = append(conditions, jobExistsCondition(jobExists))
	}
	return conditions
}

func jobExistsCondition(exists bool) metav1.Condition {
	if exists {
		return metav1.Condition{Type: ConditionJobExists, Status: metav1.ConditionTrue, Reason: "Found", Message: "the job of the build exists"}
	}
	return metav1.Condition{Type: ConditionJobExists, Status: metav1.ConditionFalse, Reason: "NotFound", Message: "the job of the build does not exist"}
}

func JobMissingSince(b v1alpha1.Build) *time.Time {
	for _, condition := range b.Status.Conditions {
		if condition.Type == ConditionJobExists && condition.Status == metav1.ConditionFalse {
			since := condition.LastTransitionTime.Time
			return &since
		}
	}
	return nil
}

func withTransitionTimes(previous []metav1.Condition, now time.Time, generation int64, conditions ...metav1.Condition) []metav1.Condition {
	out := make([]metav1.Condition, 0, len(conditions))
	for _, condition := range conditions {
		condition.ObservedGeneration = generation
		condition.LastTransitionTime = metav1.NewTime(now)
		for _, old := range previous {
			if old.Type == condition.Type && old.Status == condition.Status {
				condition.LastTransitionTime = old.LastTransitionTime
			}
		}
		out = append(out, condition)
	}
	return out
}

func BuildStatusOf(b v1alpha1.Build, gate *Gate, decision BuildDecision, jobName string, jobExists bool, now time.Time) v1alpha1.BuildStatus {
	phase, reason := decision.Phase, decision.Reason
	if gate != nil {
		phase, reason = v1alpha1.BuildPhasePending, gate.Reason
	}
	status := v1alpha1.BuildStatus{
		CommonStatus: v1alpha1.CommonStatus{
			ObservedGeneration: b.Generation,
			Conditions:         withTransitionTimes(b.Status.Conditions, now, b.Generation, conditionsOf(decision, gate, jobName, jobExists)...),
		},
		Phase:      phase,
		Reason:     reason,
		JobName:    jobName,
		StartedAt:  b.Status.StartedAt,
		FinishedAt: b.Status.FinishedAt,
	}
	status.Conditions = append(status.Conditions, previousMirrorCondition(b)...)
	if gate == nil && decision.StartedAt != nil && status.StartedAt == nil {
		started := metav1.NewTime(*decision.StartedAt)
		status.StartedAt = &started
	}
	if gate == nil && decision.Terminal() && status.FinishedAt == nil {
		finished := metav1.NewTime(*firstNonNil(decision.FinishedAt, &now))
		status.FinishedAt = &finished
	}
	if gate == nil && decision.Phase == v1alpha1.BuildPhaseSucceeded {
		status.Image = b.Spec.Output.Image
	}
	return status
}

func previousMirrorCondition(b v1alpha1.Build) []metav1.Condition {
	for _, condition := range b.Status.Conditions {
		if condition.Type == ConditionRegistryMirrors {
			return []metav1.Condition{condition}
		}
	}
	return nil
}

func MirrorCondition(problems []string, generation int64, now time.Time) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionRegistryMirrors,
		Status:             metav1.ConditionFalse,
		Reason:             ReasonInvalidMirrors,
		Message:            "ignored entries of the Setting " + SettingBuildMirrors + ": " + strings.Join(problems, "; "),
		ObservedGeneration: generation,
		LastTransitionTime: metav1.NewTime(now),
	}
}

func firstNonNil(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
