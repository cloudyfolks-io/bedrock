package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReleaseComponent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Image   string `json:"image"`
}

type ReleaseSpec struct {
	Version    string `json:"version"`
	Image      string `json:"image"`
	K0sVersion string `json:"k0sVersion"`
	// +optional
	UpgradeFrom []string `json:"upgradeFrom,omitempty"`
	// +optional
	Components []ReleaseComponent `json:"components,omitempty"`
	// +optional
	SupportedOS []string `json:"supportedOS,omitempty"`
	// +optional
	K0sChecksums map[string]string `json:"k0sChecksums,omitempty"`
	// +optional
	BedrockChecksums map[string]string `json:"bedrockChecksums,omitempty"`
	// +optional
	Images []string `json:"images,omitempty"`
}

type ReleaseStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="K0s",type=string,JSONPath=`.spec.k0sVersion`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="release spec is immutable"
type Release struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReleaseSpec   `json:"spec"`
	Status            ReleaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Release `json:"items"`
}

func (r *Release) AllowsUpgradeFrom(version string) bool {
	return slices.Contains(r.Spec.UpgradeFrom, version)
}

func init() {
	SchemeBuilder.Register(&Release{}, &ReleaseList{})
}
