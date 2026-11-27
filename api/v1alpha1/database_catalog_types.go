package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DatabaseEnginePostgreSQL = "postgresql"
	DatabaseEngineMySQL      = "mysql"
	DatabaseEngineValkey     = "valkey"
)

// +kubebuilder:validation:XValidation:rule="self.image.contains('@sha256:')",message="image must be pinned by digest with @sha256:"
// +kubebuilder:validation:XValidation:rule="!has(self.upgradeTo) || self.upgradeTo.all(m, m != self.major)",message="upgradeTo must not list the major of this version"
// +kubebuilder:validation:XValidation:rule="!has(self.companions) || self.companions.all(k, self.companions[k].contains('@sha256:'))",message="every companion image must be pinned by digest with @sha256:"
type DatabaseVersionSpec struct {
	// +kubebuilder:validation:Enum=postgresql;mysql;valkey
	Engine string `json:"engine"`
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	Major string `json:"major"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Image string `json:"image"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
	// +kubebuilder:validation:Format=date
	// +kubebuilder:validation:MaxLength=10
	// +optional
	EOLDate string `json:"eolDate,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=16
	// +kubebuilder:validation:items:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	// +listType=set
	UpgradeTo []string `json:"upgradeTo,omitempty"`
	// +kubebuilder:validation:MaxProperties=16
	// +optional
	Companions map[string]string `json:"companions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == self.spec.engine + '-' + self.spec.major",message="the name must be <engine>-<major>"
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.engine`
// +kubebuilder:printcolumn:name="Major",type=string,JSONPath=`.spec.major`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
type DatabaseVersion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DatabaseVersionSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type DatabaseVersionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseVersion `json:"items"`
}

// +kubebuilder:validation:XValidation:rule="self.maxStorageGi >= self.minStorageGi",message="maxStorageGi must not be below minStorageGi"
type DatabaseClassSpec struct {
	CPU    resource.Quantity `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
	// +kubebuilder:validation:Minimum=1
	MinStorageGi int32 `json:"minStorageGi"`
	// +kubebuilder:validation:Minimum=1
	MaxStorageGi int32 `json:"maxStorageGi"`
	// +kubebuilder:default=false
	HighAvailability bool `json:"highAvailability"`
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="the name must be a DNS label"
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.spec.cpu`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.memory`
// +kubebuilder:printcolumn:name="HA",type=boolean,JSONPath=`.spec.highAvailability`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
type DatabaseClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DatabaseClassSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type DatabaseClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DatabaseVersion{}, &DatabaseVersionList{}, &DatabaseClass{}, &DatabaseClassList{})
}
