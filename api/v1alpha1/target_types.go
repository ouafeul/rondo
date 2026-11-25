package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TargetSpec is a deployment destination: a Kubernetes cluster or a vSphere environment.
// +kubebuilder:validation:XValidation:rule="has(self.kubernetes) != has(self.vsphere)",message="exactly one of kubernetes or vsphere"
type TargetSpec struct {
	// names an Enrollment's spec.target may use besides the Target's own
	// +optional
	Roles []string `json:"roles,omitempty"`

	// site values referenced via ${target.values.<key>}
	// +optional
	Values map[string]string `json:"values,omitempty"`

	// +optional
	Kubernetes *KubernetesCluster `json:"kubernetes,omitempty"`

	// +optional
	VSphere *VSpherePlacement `json:"vsphere,omitempty"`
}

// KubernetesCluster is reached in-cluster or through a kubeconfig Secret.
// +kubebuilder:validation:XValidation:rule="(has(self.inCluster) && self.inCluster) != has(self.kubeconfigSecretRef)",message="exactly one of inCluster or kubeconfigSecretRef"
type KubernetesCluster struct {
	// the management cluster itself, via the controller's own ServiceAccount
	// +optional
	InCluster bool `json:"inCluster,omitempty"`

	// informational
	// +kubebuilder:validation:Pattern=`^https://.+$`
	// +optional
	Server string `json:"server,omitempty"`

	// +optional
	KubeconfigSecretRef *SecretKeySelector `json:"kubeconfigSecretRef,omitempty"`
}

type TargetStatus struct {
	// Ready (reachable and authenticated), Stalled.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Roles",type=string,JSONPath=`.spec.roles`
// +kubebuilder:printcolumn:name="Server",type=string,JSONPath=`.spec.kubernetes.server`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Message",type=string,priority=1,JSONPath=`.status.conditions[?(@.type=="Ready")].message`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Target is where Enrollments deploy Components.
type Target struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TargetSpec   `json:"spec,omitempty"`
	Status TargetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Target `json:"items"`
}

// Answers reports whether an Enrollment's spec.target designates this Target.
func (t *Target) Answers(name string) bool {
	return t.Name == name || slices.Contains(t.Spec.Roles, name)
}

func (t TargetSpec) InCluster() bool { return t.Kubernetes != nil && t.Kubernetes.InCluster }

func (t TargetSpec) AcceptsArchetype(a Archetype) bool {
	if t.VSphere != nil {
		return a == ArchetypeVM
	}
	return a == ArchetypeChart
}

func init() {
	SchemeBuilder.Register(&Target{}, &TargetList{})
}
