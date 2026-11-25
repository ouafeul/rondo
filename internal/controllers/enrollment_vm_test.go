package controllers

import (
	"context"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
	"rondo/internal/values"
)

func vmEnrollment(mutate func(*v1alpha1.Enrollment)) *v1alpha1.Enrollment {
	rel := &v1alpha1.Enrollment{
		ObjectMeta: metav1.ObjectMeta{Name: "mft-dmz", Namespace: "rondo-system"},
		Spec:       v1alpha1.EnrollmentSpec{Policy: v1alpha1.DeliveryPolicy{}},
	}
	if mutate != nil {
		mutate(rel)
	}
	return rel
}

func vmDesired() desired {
	return desired{
		archetype:        v1alpha1.ArchetypeVM,
		infraHash:        "infra1",
		installHash:      "cfg1",
		installerVersion: "1.4.0",
	}
}

func TestNextActionProvisionsBeforeInstalling(t *testing.T) {
	verb, phase, why := nextAction(vmEnrollment(nil), vmDesired())
	if verb != jobs.VerbVMApply {
		t.Fatalf("verb = %q, want %q", verb, jobs.VerbVMApply)
	}
	if phase != v1alpha1.PhaseProvision {
		t.Errorf("phase = %q, want Provision", phase)
	}
	if why == "" {
		t.Error("empty reason: the operator log uses it")
	}
}

func TestNextActionInstallsOnceProvisioned(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "mft-dmz-machines"}
	})
	verb, phase, _ := nextAction(rel, vmDesired())
	if verb != jobs.VerbVMInstall || phase != v1alpha1.PhaseInstall {
		t.Fatalf("verb/phase = %q/%q, want vm-install/Install", verb, phase)
	}
}

func TestNextActionReprovisionsWithoutInventory(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
	})
	verb, phase, why := nextAction(rel, vmDesired())
	if verb != jobs.VerbVMApply || phase != v1alpha1.PhaseProvision {
		t.Fatalf("verb/phase = %q/%q", verb, phase)
	}
	if why == "" {
		t.Error("empty reason")
	}
}

func TestNextActionSteadyState(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
		r.Status.AppliedInstallerVersion = "1.4.0"
		r.Status.AppliedChartVersion = "cfg1"
	})
	if verb, _, _ := nextAction(rel, vmDesired()); verb != "" {
		t.Fatalf("verb = %q, want no action", verb)
	}
}

func TestNextActionBumpInstallerDoesNotRecreateMachines(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
		r.Status.AppliedInstallerVersion = "1.3.0"
		r.Status.AppliedChartVersion = "cfg1"
	})
	verb, phase, _ := nextAction(rel, vmDesired())
	if verb != jobs.VerbVMInstall {
		t.Fatalf("verb = %q, want vm-install (and ABOVE ALL not vm-apply)", verb)
	}
	if phase != v1alpha1.PhaseInstall {
		t.Errorf("phase = %q", phase)
	}
}

func TestNextActionSizingChangeDoesNotReplayInstall(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra0"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
		r.Status.AppliedInstallerVersion = "1.4.0"
		r.Status.AppliedChartVersion = "cfg1"
	})
	verb, phase, _ := nextAction(rel, vmDesired())
	if verb != jobs.VerbVMApply || phase != v1alpha1.PhaseProvision {
		t.Fatalf("verb/phase = %q/%q, want vm-apply/Provision", verb, phase)
	}
}

func TestNextActionConfigChangeReplaysInstall(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
		r.Status.AppliedInstallerVersion = "1.4.0"
		r.Status.AppliedChartVersion = "cfg0"
	})
	verb, _, why := nextAction(rel, vmDesired())
	if verb != jobs.VerbVMInstall {
		t.Fatalf("verb = %q", verb)
	}
	if why == "" {
		t.Error("empty reason")
	}
}

func TestNextActionNoInstallerStopsAfterProvision(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
	})
	d := vmDesired()
	d.installerVersion = ""
	if verb, _, _ := nextAction(rel, d); verb != "" {
		t.Fatalf("verb = %q, want no action", verb)
	}
}

func TestConvergedVMReleaseReplaysNothing(t *testing.T) {
	rel := vmEnrollment(func(r *v1alpha1.Enrollment) {
		r.Status.AppliedValuesHash = "infra1"
		r.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: "m"}
		r.Status.AppliedInstallerVersion = "1.4.0"
		r.Status.AppliedChartVersion = "cfg1"
	})
	if verb, _, why := nextAction(rel, vmDesired()); verb != "" {
		t.Fatalf("verb = %q (%s) on a converged Enrollment: a periodic pass "+
			"would replay the install on machines in service", verb, why)
	}
}

func TestNextActionChartApplies(t *testing.T) {
	rel := &v1alpha1.Enrollment{}
	d := desired{archetype: v1alpha1.ArchetypeChart, chartVersion: "1.2.3", valuesHash: "h1"}
	verb, phase, _ := nextAction(rel, d)
	if verb != jobs.VerbHelmApply {
		t.Fatalf("verb = %q, want helm-apply", verb)
	}
	if phase != "" {
		t.Errorf("phase = %q, a chart Enrollment has none", phase)
	}
}

func TestNextActionChartSteadyState(t *testing.T) {
	rel := &v1alpha1.Enrollment{}
	rel.Status.AppliedChartVersion = "1.2.3"
	rel.Status.AppliedValuesHash = "h1"
	d := desired{archetype: v1alpha1.ArchetypeChart, chartVersion: "1.2.3", valuesHash: "h1"}
	if verb, _, _ := nextAction(rel, d); verb != "" {
		t.Fatalf("verb = %q, want no action", verb)
	}
}

func vmSpecFixture() *v1alpha1.VMSpec {
	return &v1alpha1.VMSpec{
		Template: "rhel9",
		Sizing:   &v1alpha1.VMSizing{CPU: 4, MemoryMB: 8192, DiskGB: 100},
		Groups: []v1alpha1.VMGroup{
			{Name: "app", Replicas: 2},
		},
		Config: map[string]v1alpha1.VMConfigValue{
			"K": {Value: ptrStr("v")},
		},
		Installer: &v1alpha1.VMInstaller{Script: "install.sh", Version: "1.0.0"},
	}
}

func ptrStr(s string) *string { return &s }

func fixtureTarget(ips string) *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "dmz"},
		Spec:       v1alpha1.TargetSpec{Values: map[string]string{"vmIPs.demo.app": ips}},
	}
}

func (h *testHarness) createVMComponent(t *testing.T, name, installerVersion string,
	dependsOn ...string) *v1alpha1.Component {
	t.Helper()
	comp := &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec: v1alpha1.ComponentSpec{VM: &v1alpha1.VMSpec{
			Template: "rhel9-cloudinit",
			Groups:   []v1alpha1.VMGroup{{Name: "app", Replicas: 1}},
		}},
	}
	if installerVersion != "" {
		comp.Spec.VM.Installer = &v1alpha1.VMInstaller{
			Script: "deploy/install.sh", Version: installerVersion,
		}
	}
	for _, d := range dependsOn {
		comp.Spec.DependsOn = append(comp.Spec.DependsOn, v1alpha1.LocalObjectReference{Name: d})
	}
	if err := h.client.Create(context.Background(), comp); err != nil {
		t.Fatalf("creating Component %s: %v", name, err)
	}
	return comp
}

func setupVMRelease(t *testing.T, h *testHarness, installerVersion string) string {
	t.Helper()
	h.createVSphereTarget(t, "dmz", nil,
		map[string]string{"vmIPs.mft.app": `["10.0.2.10"]`, "vmTemplate": "rhel9-cloudinit"})
	h.createVMComponent(t, "mft", installerVersion)
	h.createEnrollment(t, "mft-dmz", "mft", "dmz", "", nil)
	return "mft-dmz"
}

func (h *testHarness) runPhase(t *testing.T, name string, wantVerb jobs.Verb, before int,
	duringJob func()) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile (%s): %v", wantVerb, err)
	}
	js := h.jobsFor(t, name)
	if len(js) != before+1 {
		t.Fatalf("%d Job(s) after phase %s, want %d", len(js), wantVerb, before+1)
	}
	rel := h.getEnrollment(t, name)
	if rel.Status.ActiveJobRef == nil {
		t.Fatalf("no active Job for phase %s", wantVerb)
	}
	job := rel.Status.ActiveJobRef.Name
	var got batchv1.Job
	if err := h.client.Get(ctx, h.key(job), &got); err != nil {
		t.Fatalf("reading Job %s: %v", job, err)
	}
	if v := jobs.Verb(got.Labels[jobs.LabelVerb]); v != wantVerb {
		t.Fatalf("Job verb = %q, want %q", v, wantVerb)
	}
	if duringJob != nil {
		duringJob()
	}
	h.finishJob(t, job, true)
	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile after phase %s: %v", wantVerb, err)
	}
}

func (h *testHarness) publishMachines(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	rel := h.getEnrollment(t, name)
	rel.Status.MachinesSecretRef = &v1alpha1.LocalObjectReference{Name: name + "-machines"}
	if err := h.client.Status().Update(ctx, rel); err != nil {
		t.Fatalf("publishing the inventory: %v", err)
	}
}

func TestVMReleaseBecomesReadyWithoutHealth(t *testing.T) {
	h := newHarness(t)
	name := setupVMRelease(t, h, "33.0.0")

	h.runPhase(t, name, jobs.VerbVMApply, 0, func() { h.publishMachines(t, name) })
	rel := h.getEnrollment(t, name)
	requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionApplied, "True")
	msg := requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionReady, "False")
	if !strings.Contains(msg, "never run") {
		t.Errorf("message = %q, must say the install has not run", msg)
	}

	h.runPhase(t, name, jobs.VerbVMInstall, 1, nil)
	rel = h.getEnrollment(t, name)
	if rel.Status.AppliedInstallerVersion != "33.0.0" {
		t.Fatalf("appliedInstallerVersion = %q", rel.Status.AppliedInstallerVersion)
	}
	msg = requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionReady, "True")
	if !strings.Contains(msg, "health not probed") {
		t.Errorf("message = %q, must say health is not probed", msg)
	}
	if !strings.Contains(msg, "33.0.0") {
		t.Errorf("message = %q, must name the installed version", msg)
	}
	if rel.Status.Health != "" {
		t.Errorf("health = %q: no health may be CLAIMED for this archetype",
			rel.Status.Health)
	}
}

func TestVMReleaseWithoutInstallerIsReadyAfterProvision(t *testing.T) {
	h := newHarness(t)
	name := setupVMRelease(t, h, "")

	h.runPhase(t, name, jobs.VerbVMApply, 0, func() { h.publishMachines(t, name) })
	rel := h.getEnrollment(t, name)
	msg := requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionReady, "True")
	if !strings.Contains(msg, "no installer declared") {
		t.Errorf("message = %q", msg)
	}
	if got := len(h.jobsFor(t, name)); got != 1 {
		t.Fatalf("%d Jobs, want 1: no install may be started", got)
	}
}

func TestVMComponentSatisfiesDependsOn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupVMRelease(t, h, "33.0.0")
	h.runPhase(t, name, jobs.VerbVMApply, 0, func() { h.publishMachines(t, name) })
	h.runPhase(t, name, jobs.VerbVMInstall, 1, nil)

	vmRel := h.getEnrollment(t, name)
	requireCondition(t, vmRel.Status.Conditions, v1alpha1.ConditionReady, "True")

	h.createVMComponent(t, "next", "1.0.0", "mft")
	h.createEnrollment(t, "next", "next", "dmz", "", nil)
	if _, err := h.enrollment.Reconcile(ctx, h.req("next")); err != nil {
		t.Fatalf("Reconcile of the dependent Enrollment: %v", err)
	}
	got := h.getEnrollment(t, "next")
	if got.Status.ActiveJobRef == nil {
		msg := requireCondition(t, got.Status.Conditions, v1alpha1.ConditionReady, "False")
		t.Fatalf("the dependency on a machine component is not met: %s", msg)
	}
}

func TestTargetValueMovesTheRightFingerprint(t *testing.T) {
	target := fixtureTarget(`["10.0.0.1","10.0.0.2"]`)
	resolve := func(image, tenant string) *v1alpha1.VMSpec {
		t.Helper()
		vm := vmSpecFixture()
		vm.Template = "${target.values.vmImage}"
		vm.Config["K"] = v1alpha1.VMConfigValue{Value: ptrStr("${target.values.tenant}")}
		site := fixtureTarget(`["10.0.0.1","10.0.0.2"]`)
		site.Spec.Values["vmImage"] = image
		site.Spec.Values["tenant"] = tenant
		out, err := values.ResolveVM(vm, values.TargetVars(site))
		if err != nil {
			t.Fatalf("ResolveVM: %v", err)
		}
		return out
	}
	base := resolve("rhel9", "acme")
	otherImage := resolve("rhel10", "acme")
	otherTenant := resolve("rhel9", "globex")

	if values.VMInfraHash(base, "demo", target) == values.VMInfraHash(otherImage, "demo", target) {
		t.Error("the resolved template is not part of the infrastructure fingerprint")
	}
	if values.VMInstallHash(base, nil) != values.VMInstallHash(otherImage, nil) {
		t.Error("the template replays the install, yet it only describes the machines")
	}
	if values.VMInstallHash(base, nil) == values.VMInstallHash(otherTenant, nil) {
		t.Error("the resolved configuration is not part of the install fingerprint")
	}
	if values.VMInfraHash(base, "demo", target) != values.VMInfraHash(otherTenant, "demo", target) {
		t.Error("the configuration moves the infrastructure fingerprint, the machines would be recreated")
	}
}

func TestInstallSecretsReadsWhatExists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mft-secrets", Namespace: h.ns},
		StringData: map[string]string{"DB_PASSWORD": "v1", "OTHER": "x"},
	}); err != nil {
		t.Fatalf("creating the Secret: %v", err)
	}
	if err := h.client.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "mft-tuning", Namespace: h.ns},
		Data:       map[string]string{"WORKERS": "4"},
	}); err != nil {
		t.Fatalf("creating the ConfigMap: %v", err)
	}
	vm := &v1alpha1.VMSpec{Installer: &v1alpha1.VMInstaller{
		Script: "install.sh", Version: "1.0.0",
		SecretsFrom: []v1alpha1.ValuesSource{
			{SecretRef: &v1alpha1.LocalObjectReference{Name: "mft-secrets"}, Keys: []string{"DB_PASSWORD"}},
			{ConfigMapRef: &v1alpha1.LocalObjectReference{Name: "mft-tuning"}, Keys: []string{"WORKERS"}},
			{SecretRef: &v1alpha1.LocalObjectReference{Name: "not-created-yet"}, Keys: []string{"X"}},
		},
	}}
	got, err := h.enrollment.installSecrets(ctx, h.ns, vm)
	if err != nil {
		t.Fatalf("installSecrets: %v", err)
	}
	want := map[string]string{"DB_PASSWORD": "v1", "WORKERS": "4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("secrets = %v, want %v", got, want)
	}
	if got, err := h.enrollment.installSecrets(ctx, h.ns, &v1alpha1.VMSpec{}); err != nil || got != nil {
		t.Errorf("without installer: %v / %v", got, err)
	}
}

func TestSecretRotationReplaysInstallOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupVMRelease(t, h, "33.0.0")

	comp := &v1alpha1.Component{}
	if err := h.client.Get(ctx, h.key("mft"), comp); err != nil {
		t.Fatalf("reading the Component: %v", err)
	}
	comp.Spec.VM.Installer.SecretsFrom = []v1alpha1.ValuesSource{{
		SecretRef: &v1alpha1.LocalObjectReference{Name: "mft-secrets"},
		Keys:      []string{"DB_PASSWORD"},
	}}
	if err := h.client.Update(ctx, comp); err != nil {
		t.Fatalf("updating the Component: %v", err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mft-secrets", Namespace: h.ns},
		StringData: map[string]string{"DB_PASSWORD": "v1"},
	}
	if err := h.client.Create(ctx, sec); err != nil {
		t.Fatalf("creating the Secret: %v", err)
	}

	h.runPhase(t, name, jobs.VerbVMApply, 0, func() { h.publishMachines(t, name) })
	h.runPhase(t, name, jobs.VerbVMInstall, 1, nil)
	rel := h.getEnrollment(t, name)
	infra, install := rel.Status.AppliedValuesHash, rel.Status.AppliedChartVersion

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile in steady state: %v", err)
	}
	if got := len(h.jobsFor(t, name)); got != 2 {
		t.Fatalf("%d Jobs in steady state, want 2", got)
	}

	sec.StringData = map[string]string{"DB_PASSWORD": "v2"}
	if err := h.client.Update(ctx, sec); err != nil {
		t.Fatalf("rotating the Secret: %v", err)
	}
	h.runPhase(t, name, jobs.VerbVMInstall, 2, nil)

	rel = h.getEnrollment(t, name)
	if rel.Status.AppliedChartVersion == install {
		t.Error("the rotation did not move the install fingerprint")
	}
	if rel.Status.AppliedValuesHash != infra {
		t.Error("the rotation moved the infrastructure fingerprint, the machines would be recreated")
	}
}

func TestVMReleaseStallsOnUnknownTargetValue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := setupVMRelease(t, h, "33.0.0")

	comp := &v1alpha1.Component{}
	if err := h.client.Get(ctx, h.key("mft"), comp); err != nil {
		t.Fatalf("reading the Component: %v", err)
	}
	comp.Spec.VM.Config = map[string]v1alpha1.VMConfigValue{
		"TENANT": {Value: ptrStr("${target.values.tenant}")},
	}
	if err := h.client.Update(ctx, comp); err != nil {
		t.Fatalf("updating the Component: %v", err)
	}

	if _, err := h.enrollment.Reconcile(ctx, h.req(name)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(h.jobsFor(t, name)); got != 0 {
		t.Fatalf("%d Job(s) created: provisioning started on an unresolved token", got)
	}
	rel := h.getEnrollment(t, name)
	msg := requireCondition(t, rel.Status.Conditions, v1alpha1.ConditionStalled, "True")
	if !strings.Contains(msg, "target.values.tenant") {
		t.Errorf("message = %q, must name the token", msg)
	}
}

func TestTargetValueChangeMovesTheRightPhase(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createVSphereTarget(t, "dmz", nil,
		map[string]string{
			"vmIPs.mft.app": `["10.0.2.10"]`,
			"vmTemplate":    "rhel9-cloudinit",
			"baseImage":     "rhel9-cloudinit",
			"tenant":        "acme",
		})
	comp := h.createVMComponent(t, "mft", "33.0.0")
	comp.Spec.VM.Template = "${target.values.baseImage}"
	comp.Spec.VM.Config = map[string]v1alpha1.VMConfigValue{
		"TENANT": {Value: ptrStr("${target.values.tenant}")},
	}
	if err := h.client.Update(ctx, comp); err != nil {
		t.Fatalf("updating the Component: %v", err)
	}
	h.createEnrollment(t, "mft-dmz", "mft", "dmz", "", nil)
	if _, err := h.enrollment.Reconcile(ctx, h.req("mft-dmz")); err != nil {
		t.Fatalf("Reconcile of the enrollment: %v", err)
	}
	name := "mft-dmz"

	h.runPhase(t, name, jobs.VerbVMApply, 0, func() { h.publishMachines(t, name) })
	h.runPhase(t, name, jobs.VerbVMInstall, 1, nil)
	infra := h.getEnrollment(t, name).Status.AppliedValuesHash

	h.setTargetValue(t, "dmz", "tenant", "globex")
	h.runPhase(t, name, jobs.VerbVMInstall, 2, nil)
	if got := h.getEnrollment(t, name).Status.AppliedValuesHash; got != infra {
		t.Fatal("a configuration value moved the infrastructure fingerprint")
	}

	h.setTargetValue(t, "dmz", "baseImage", "rhel10-cloudinit")
	h.runPhase(t, name, jobs.VerbVMApply, 3, nil)
	if got := h.getEnrollment(t, name).Status.AppliedValuesHash; got == infra {
		t.Error("the site template did not move the infrastructure fingerprint")
	}
}

func (h *testHarness) setTargetValue(t *testing.T, name, key, value string) {
	t.Helper()
	var target v1alpha1.Target
	if err := h.client.Get(context.Background(), h.key(name), &target); err != nil {
		t.Fatalf("reading Target %s: %v", name, err)
	}
	target.Spec.Values[key] = value
	if err := h.client.Update(context.Background(), &target); err != nil {
		t.Fatalf("updating Target %s: %v", name, err)
	}
}
