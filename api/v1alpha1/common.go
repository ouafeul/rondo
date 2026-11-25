package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionReady       = "Ready"
	ConditionReconciling = "Reconciling"
	ConditionStalled     = "Stalled"
	ConditionApplied     = "Applied"
)

const (
	ReasonSucceeded         = "Succeeded"
	ReasonFailed            = "Failed"
	ReasonProgressing       = "Progressing"
	ReasonDependencyPending = "DependencyPending"
	ReasonTargetUnreachable = "TargetUnreachable"
	ReasonJobRunning        = "JobRunning"
	ReasonLeaseLost         = "LeaseLost"
)

const EnrollmentFinalizer = "rondo.ouafeul.github.io/enrollment-uninstall"

// LocalObjectReference names an object in the same namespace.
type LocalObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type SecretKeySelector struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// ValuesSource injects values from a ConfigMap or a Secret.
type ValuesSource struct {
	// +optional
	ConfigMapRef *LocalObjectReference `json:"configMapRef,omitempty"`
	// +optional
	SecretRef *LocalObjectReference `json:"secretRef,omitempty"`
	// empty is a refusal, not a wildcard
	// +kubebuilder:validation:MinItems=1
	Keys []string `json:"keys"`
}

// InventoryEntry is one object delivered by an apply.
type InventoryEntry struct {
	// "" for core
	// +optional
	Group   string `json:"group,omitempty"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	// empty for a cluster-scoped resource
	// +optional
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// AttemptInfo is one Job attempt.
type AttemptInfo struct {
	JobName string `json:"jobName"`
	// chart or module version
	// +optional
	Version   string      `json:"version,omitempty"`
	StartedAt metav1.Time `json:"startedAt"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +kubebuilder:validation:Enum=Succeeded;Failed;TimedOut;Aborted
	Result string `json:"result"`
	// failure cause
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	Message string `json:"message,omitempty"`
}

const MaxHistory = 5

// DeliveryPolicy controls how an Enrollment is applied.
type DeliveryPolicy struct {
	// downstream bootstrap glue: its CRDs arrive in parallel
	// +kubebuilder:default=false
	// +optional
	SkipDryRunOnMissingCRD bool `json:"skipDryRunOnMissingCRD,omitempty"`
	// +kubebuilder:default=true
	// +optional
	CreateNamespace *bool `json:"createNamespace,omitempty"`
	// avoids a release stuck in pending-upgrade
	// +kubebuilder:default=true
	// +optional
	Atomic *bool `json:"atomic,omitempty"`
	// +kubebuilder:default=true
	// +optional
	Wait *bool `json:"wait,omitempty"`
	// +kubebuilder:default="10m"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

func (p DeliveryPolicy) CreateNamespaceEnabled() bool { return boolOr(p.CreateNamespace, true) }

func (p DeliveryPolicy) AtomicEnabled() bool { return boolOr(p.Atomic, true) }

func (p DeliveryPolicy) WaitEnabled() bool { return boolOr(p.Wait, true) }

func (p DeliveryPolicy) TimeoutOr(def time.Duration) time.Duration {
	if p.Timeout == nil || p.Timeout.Duration <= 0 {
		return def
	}
	return p.Timeout.Duration
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func Bool(b bool) *bool { return &b }

const SubstitutionPrefix = "target."
