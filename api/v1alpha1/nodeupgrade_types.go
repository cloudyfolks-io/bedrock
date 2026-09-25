package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	StepPreload     = "Preload"
	StepBackup      = "Backup"
	StepAgentUpdate = "AgentUpdate"
	StepOSUpdate    = "OSUpdate"
	StepReboot      = "Reboot"
	StepPrune       = "Prune"
	StepCleanup     = "Cleanup"
	StepRestore     = "Restore"

	StepPending   = "Pending"
	StepRunning   = "Running"
	StepSucceeded = "Succeeded"
	StepFailed    = "Failed"
)

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.steps) || (has(self.steps) && oldSelf.steps.all(s, s in self.steps))",message="steps may only be appended"
type NodeUpgradeSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="node is immutable"
	Node string `json:"node"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="version is immutable"
	Version string `json:"version"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="from is immutable"
	From string `json:"from"`
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="depot is immutable"
	Depot string `json:"depot,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="backup is set once"
	Backup string `json:"backup,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self >= oldSelf",message="attempt may only increase"
	Attempt int32 `json:"attempt"`
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=16
	// +kubebuilder:validation:items:Enum=Preload;Backup;AgentUpdate;OSUpdate;Reboot;Prune;Cleanup;Restore
	Steps []string `json:"steps,omitempty"`
}

type NodeUpgradeStepStatus struct {
	Name string `json:"name"`
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	State string `json:"state"`
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

type NodeUpgradeStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	BootID string `json:"bootID,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=name
	Steps []NodeUpgradeStepStatus `json:"steps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=`.spec.node`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.node`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
type NodeUpgrade struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NodeUpgradeSpec   `json:"spec"`
	Status            NodeUpgradeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NodeUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeUpgrade `json:"items"`
}

func NodeUpgradeName(version, node string) string {
	return version + "-" + node
}

func (n *NodeUpgrade) StepState(name string) string {
	for _, step := range n.Status.Steps {
		if step.Name == name {
			return step.State
		}
	}
	return ""
}

func init() {
	SchemeBuilder.Register(&NodeUpgrade{}, &NodeUpgradeList{})
}
