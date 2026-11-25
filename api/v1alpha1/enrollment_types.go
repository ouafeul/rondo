package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnrollmentSpec places a Component on exactly one Target.
type EnrollmentSpec struct {
	// +kubebuilder:validation:MinLength=1
	Component string `json:"component"`

	// a Target name or role; must match exactly one Target
	// +kubebuilder:validation:MinLength=1
	Target string `json:"target"`

	// overlay on the Component values
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// +optional
	ValuesFrom []ValuesSource `json:"valuesFrom,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Policy DeliveryPolicy `json:"policy,omitempty"`
}

// Phase is the current step of a VM Enrollment.
// +kubebuilder:validation:Enum=Provision;Install
type Phase string

const (
	PhaseProvision Phase = "Provision"
	PhaseInstall   Phase = "Install"
)

type EnrollmentStatus struct {
	// Ready, Applied, Reconciling, Stalled.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// the Target spec.target resolved to
	// +optional
	ResolvedTarget string `json:"resolvedTarget,omitempty"`

	// +optional
	AppliedChartVersion string `json:"appliedChartVersion,omitempty"`

	// +optional
	Phase Phase `json:"phase,omitempty"`

	// +optional
	AppliedInstallerVersion string `json:"appliedInstallerVersion,omitempty"`

	// machine module outputs: Ansible inventory, IPs per group
	// +optional
	MachinesSecretRef *LocalObjectReference `json:"machinesSecretRef,omitempty"`

	// compared to the resolved values to spot a configuration change
	// +optional
	AppliedValuesHash string `json:"appliedValuesHash,omitempty"`

	// the in-flight Job
	// +optional
	ActiveJobRef *LocalObjectReference `json:"activeJobRef,omitempty"`

	// +optional
	AttemptCount int64 `json:"attemptCount,omitempty"`

	// +optional
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`

	// objects delivered by the last apply
	// +optional
	Inventory []InventoryEntry `json:"inventory,omitempty"`

	// len(Inventory)
	// +optional
	InventoryCount int `json:"inventoryCount,omitempty"`

	// from the per-GVK table (internal/probe)
	// +kubebuilder:validation:Enum=Healthy;Progressing;Degraded;Unknown
	// +optional
	Health string `json:"health,omitempty"`

	// names the offending object
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	HealthMessage string `json:"healthMessage,omitempty"`

	// +optional
	LastAttempt *AttemptInfo `json:"lastAttempt,omitempty"`

	// +kubebuilder:validation:MaxItems=5
	// +optional
	History []AttemptInfo `json:"history,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

const (
	HealthHealthy     = "Healthy"
	HealthProgressing = "Progressing"
	HealthDegraded    = "Degraded"
	HealthUnknown     = "Unknown"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=enroll
// +kubebuilder:printcolumn:name="Component",type=string,JSONPath=`.spec.component`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.status.resolvedTarget`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.appliedChartVersion`
// +kubebuilder:printcolumn:name="Objects",type=integer,priority=1,JSONPath=`.status.inventoryCount`
// +kubebuilder:printcolumn:name="Health",type=string,JSONPath=`.status.health`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Message",type=string,priority=1,JSONPath=`.status.conditions[?(@.type=="Ready")].message`
// +kubebuilder:printcolumn:name="Phase",type=string,priority=1,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Enrollment deploys a Component on a Target.
type Enrollment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnrollmentSpec   `json:"spec,omitempty"`
	Status EnrollmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EnrollmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Enrollment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Enrollment{}, &EnrollmentList{})
}
