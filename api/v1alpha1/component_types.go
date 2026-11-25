package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChartSource points at a Helm chart on the air-gap OCI registry.
type ChartSource struct {
	// full chart URL, name included
	// +kubebuilder:validation:Pattern=`^oci://.+$`
	OCI string `json:"oci"`

	// exact, never a range
	// +kubebuilder:validation:Pattern=`^v?[0-9]+\.[0-9]+\.[0-9]+([-+].*)?$`
	Version string `json:"version"`

	// published by the component's own CI
	// +optional
	Published *bool `json:"published,omitempty"`
}

// ComponentSpec is a deployable workload, placed on Targets by an Enrollment.
// +kubebuilder:validation:XValidation:rule="has(self.chart) != has(self.vm)",message="exactly one of chart (archetype A) or vm (archetypes B/C)"
// +kubebuilder:validation:XValidation:rule="!has(self.chart) || has(self.__namespace__)",message="namespace required for a chart component"
type ComponentSpec struct {
	// +optional
	Chart *ChartSource `json:"chart,omitempty"`

	// +optional
	VM *VMSpec `json:"vm,omitempty"`

	// required for a chart component, ignored for a VM one
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// empty => the Component name
	// +kubebuilder:validation:MaxLength=53
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`

	// may carry ${target.*} tokens, resolved per target
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// for secret values that must not be committed
	// +optional
	ValuesFrom []ValuesSource `json:"valuesFrom,omitempty"`

	// applied on a target only once these are healthy there
	// +optional
	DependsOn []LocalObjectReference `json:"dependsOn,omitempty"`

	// health rules for shapes internal/probe does not model
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Health []HealthRule `json:"health,omitempty"`
}

// HealthRule judges one object shape shipped by the component.
type HealthRule struct {
	// empty = core group
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Group string `json:"group,omitempty"`

	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// empty => healthy once applied
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Condition string `json:"condition,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=comp
// +kubebuilder:printcolumn:name="Chart",type=string,JSONPath=`.spec.chart.oci`,priority=1
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.chart.version`
// +kubebuilder:printcolumn:name="VM",type=string,priority=1,JSONPath=`.spec.vm.groups[*].name`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Component says what to deploy. An Enrollment says where.
type Component struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ComponentSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type ComponentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Component `json:"items"`
}

func (c ComponentSpec) Archetype() Archetype {
	if c.VM != nil {
		return ArchetypeVM
	}
	return ArchetypeChart
}

type Archetype string

const (
	ArchetypeChart Archetype = "chart"
	ArchetypeVM    Archetype = "vm"
)

func init() {
	SchemeBuilder.Register(&Component{}, &ComponentList{})
}
