package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestDeliveryPolicyDefaults(t *testing.T) {
	var zero DeliveryPolicy
	if !zero.CreateNamespaceEnabled() || !zero.AtomicEnabled() || !zero.WaitEnabled() {
		t.Error("an empty policy must mean true for createNamespace, atomic and wait")
	}
	off := DeliveryPolicy{CreateNamespace: Bool(false), Atomic: Bool(false), Wait: Bool(false)}
	if off.CreateNamespaceEnabled() || off.AtomicEnabled() || off.WaitEnabled() {
		t.Error("an explicit false must disable the field")
	}
	if got := zero.TimeoutOr(9 * time.Minute); got != 9*time.Minute {
		t.Errorf("TimeoutOr without timeout = %v", got)
	}
	p := DeliveryPolicy{Timeout: &metav1.Duration{Duration: time.Minute}}
	if got := p.TimeoutOr(9 * time.Minute); got != time.Minute {
		t.Errorf("TimeoutOr = %v", got)
	}
}

func TestTargetAnswersItsNameAndRoles(t *testing.T) {
	tg := &Target{ObjectMeta: metav1.ObjectMeta{Name: "rke2-a"},
		Spec: TargetSpec{Roles: []string{"monitoring"}}}
	for name, want := range map[string]bool{"rke2-a": true, "monitoring": true, "dmz": false} {
		if got := tg.Answers(name); got != want {
			t.Errorf("Answers(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAcceptsArchetype(t *testing.T) {
	k8s := TargetSpec{Kubernetes: &KubernetesCluster{InCluster: true}}
	vsphere := TargetSpec{VSphere: &VSpherePlacement{}}
	cases := []struct {
		spec TargetSpec
		arch Archetype
		want bool
	}{
		{k8s, ArchetypeChart, true},
		{k8s, ArchetypeVM, false},
		{vsphere, ArchetypeVM, true},
		{vsphere, ArchetypeChart, false},
	}
	for _, tc := range cases {
		if got := tc.spec.AcceptsArchetype(tc.arch); got != tc.want {
			t.Errorf("AcceptsArchetype(%q) = %v, want %v", tc.arch, got, tc.want)
		}
	}
}

func TestArchetype(t *testing.T) {
	if got := (ComponentSpec{Chart: &ChartSource{}}).Archetype(); got != ArchetypeChart {
		t.Errorf("archetype of a chart component = %q", got)
	}
	if got := (ComponentSpec{VM: &VMSpec{}}).Archetype(); got != ArchetypeVM {
		t.Errorf("archetype of a machine component = %q", got)
	}
}

func TestVMInstallerAccessors(t *testing.T) {
	var zero VMInstaller
	if !zero.BecomeEnabled() || zero.StepsFlagOr() != "--steps" || zero.SkipFlagOr() != "--skip" {
		t.Error("wrong installer defaults")
	}
	custom := VMInstaller{Become: Bool(false), StepsFlag: "-s", SkipFlag: "-S"}
	if custom.BecomeEnabled() || custom.StepsFlagOr() != "-s" || custom.SkipFlagOr() != "-S" {
		t.Error("explicit installer values ignored")
	}
	if !(RPMRepo{}).GPGCheckEnabled() || (RPMRepo{GPGCheck: Bool(false)}).GPGCheckEnabled() {
		t.Error("gpgCheck: default true, explicit false honored")
	}
}

func TestGroupVersionRegistered(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for _, kind := range []string{"Component", "Target", "Enrollment"} {
		if _, err := s.New(GroupVersion.WithKind(kind)); err != nil {
			t.Errorf("kind %s not registered: %v", kind, err)
		}
	}
}
