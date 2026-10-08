package compute

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Gate struct{ Reason, Message string }

type ObservedVM struct {
	PrintableStatus, Message, NodeName string
	VMConditions, VMIConditions        map[string]string
}

var (
	reasonsBeforeNetwork = []string{ReasonSubnetNotReady, ReasonSecurityGroupNotReady, ReasonWaitingForDefaultGroup}
	reasonsAfterNetwork  = []string{ReasonSSHKeyNotReady, ReasonInvalidUserData, ReasonVolumeNotFound, ReasonVolumeInUse, ReasonVolumeNotUsable, ReasonApplyFailed, ReasonNameConflict}
)

func Phase(printableStatus string, desiredRunning bool) string {
	switch printableStatus {
	case "Running", "Paused":
		return v1alpha1.VMPhaseRunning
	case "Migrating":
		return v1alpha1.VMPhaseMigrating
	case "Stopped", "Starting", "Provisioning", "WaitingForVolumeBinding", "":
		return startingOrStopped(desiredRunning)
	case "Stopping", "Terminating":
		return v1alpha1.VMPhaseStopping
	default:
		return v1alpha1.VMPhaseFailed
	}
}

func startingOrStopped(desiredRunning bool) string {
	if desiredRunning {
		return v1alpha1.VMPhaseStarting
	}
	return v1alpha1.VMPhaseStopped
}

func ConsolePaths(namespace, name string) v1alpha1.VMConsole {
	base := "/apis/" + v1alpha1.GroupVersion.String() + "/namespaces/" + namespace + "/virtualmachines/" + name
	return v1alpha1.VMConsole{VNCPath: base + "/console/vnc", SerialPath: base + "/console/serial"}
}

func ConsoleFor(phase, namespace, name string) *v1alpha1.VMConsole {
	if phase != v1alpha1.VMPhaseRunning && phase != v1alpha1.VMPhaseMigrating {
		return nil
	}
	paths := ConsolePaths(namespace, name)
	return &paths
}

func AddressesOf(pods []corev1.Pod, nodeName, namespace string, subnets []string) []v1alpha1.VMAddress {
	pod, found := preferredPod(pods, nodeName)
	if !found {
		return nil
	}
	var out []v1alpha1.VMAddress
	for _, subnet := range subnets {
		prefix := AttachmentProvider(namespace, subnet) + ".cloudyfolks.io/"
		ips := pod.Annotations[prefix+"ip_address"]
		if ips == "" {
			continue
		}
		for _, ip := range strings.Split(ips, ",") {
			out = append(out, v1alpha1.VMAddress{Subnet: subnet, IP: strings.TrimSpace(ip), MAC: pod.Annotations[prefix+"mac_address"]})
		}
	}
	return out
}

func preferredPod(pods []corev1.Pod, nodeName string) (corev1.Pod, bool) {
	if len(pods) == 0 {
		return corev1.Pod{}, false
	}
	ranked := slices.Clone(pods)
	sort.SliceStable(ranked, func(i, j int) bool {
		return podRank(ranked[i], nodeName) > podRank(ranked[j], nodeName)
	})
	return ranked[0], true
}

func podRank(pod corev1.Pod, nodeName string) int {
	rank := 0
	if pod.DeletionTimestamp == nil {
		rank += 2
	}
	if nodeName != "" && pod.Spec.NodeName == nodeName {
		rank++
	}
	return rank
}

func GuestAgentFrom(info []byte) (v1alpha1.VMGuestAgent, error) {
	var parsed struct {
		Version  string `json:"guestAgentVersion"`
		Hostname string `json:"hostname"`
		OS       struct {
			PrettyName string `json:"prettyName"`
		} `json:"os"`
	}
	if err := json.Unmarshal(info, &parsed); err != nil {
		return v1alpha1.VMGuestAgent{}, err
	}
	return v1alpha1.VMGuestAgent{Version: parsed.Version, OS: parsed.OS.PrettyName, Hostname: parsed.Hostname}, nil
}

func Conditions(generation int64, phase string, desiredRunning bool, gate *Gate, observed ObservedVM) []metav1.Condition {
	conditions := []metav1.Condition{readyCondition(phase, gate, observed)}
	if network, present := networkCondition(gate); present {
		conditions = append(conditions, network)
	}
	if observed.VMIConditions != nil {
		conditions = append(conditions,
			observedCondition(ConditionGuestAgent, observed.VMIConditions["AgentConnected"], "Connected", "NotConnected"),
			observedCondition(ConditionLiveMigratable, observed.VMIConditions["LiveMigratable"], "Migratable", "NotMigratable"),
		)
	}
	if observed.VMConditions["RestartRequired"] == string(metav1.ConditionTrue) {
		conditions = append(conditions, metav1.Condition{Type: ConditionRestartRequired, Status: metav1.ConditionTrue, Reason: ConditionRestartRequired, Message: "a restart applies pending changes"})
	}
	for index := range conditions {
		conditions[index].ObservedGeneration = generation
	}
	sort.Slice(conditions, func(i, j int) bool { return conditions[i].Type < conditions[j].Type })
	return conditions
}

func readyCondition(phase string, gate *Gate, observed ObservedVM) metav1.Condition {
	cond := metav1.Condition{Type: v1alpha1.ConditionReady}
	if gate != nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, gate.Reason, gate.Message
		return cond
	}
	switch phase {
	case v1alpha1.VMPhaseRunning:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, ReasonRunning, "the virtual machine is running"
	case v1alpha1.VMPhaseMigrating:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, ReasonMigrating, "the virtual machine is migrating"
	case v1alpha1.VMPhaseStopped:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, ReasonStopped, "the virtual machine is stopped"
	case v1alpha1.VMPhaseStopping:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, ReasonStopping, "the virtual machine is stopping"
	case v1alpha1.VMPhaseFailed:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, ReasonFailed, "the virtual machine failed: "+observed.Message
	default:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, ReasonStarting, "the virtual machine is starting"
	}
	return cond
}

func networkCondition(gate *Gate) (metav1.Condition, bool) {
	cond := metav1.Condition{Type: ConditionNetworkReady}
	switch {
	case gate == nil || slices.Contains(reasonsAfterNetwork, gate.Reason):
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "Attached", "the networks are ready"
	case slices.Contains(reasonsBeforeNetwork, gate.Reason):
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, gate.Reason, gate.Message
	default:
		return metav1.Condition{}, false
	}
	return cond, true
}

func observedCondition(conditionType, value, whenTrue, whenFalse string) metav1.Condition {
	cond := metav1.Condition{Type: conditionType, Status: metav1.ConditionFalse, Reason: whenFalse, Message: whenFalse}
	if value == string(metav1.ConditionTrue) {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, whenTrue, whenTrue
	}
	return cond
}
