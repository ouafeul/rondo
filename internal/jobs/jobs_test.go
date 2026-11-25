package jobs

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"rondo/api/v1alpha1"
)

func testConfig() Config {
	return Config{
		Namespace:         "rondo-system",
		Image:             "arti.local/rondo-docker-local/rondo-toolbox:0.1.0",
		ServiceAccount:    "rondo-exec",
		MirrorCAConfigMap: "mirror-ca",
		MirrorCredsSecret: "rondo-credentials",
	}
}

func testRelease(name string) *v1alpha1.Enrollment {
	return &v1alpha1.Enrollment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "rondo-system",
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
	}
}

var releaseGVK = schema.GroupVersionKind{
	Group: "rondo.ouafeul.github.io", Version: "v1alpha1", Kind: "Enrollment",
}

func testRequest(name string, fp string) Request {
	return Request{
		Verb:        VerbHelmApply,
		Owner:       testRelease(name),
		OwnerGVK:    releaseGVK,
		Fingerprint: fp,
	}
}

func TestBuildEnforcesNoRetry(t *testing.T) {
	job, err := testConfig().Build(testRequest("metallb-acme", "gen1"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("backoffLimit = %v, want 0 (requeueing belongs to the reconciler)", job.Spec.BackoffLimit)
	}
	if got := job.Spec.Template.Spec.RestartPolicy; got != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", got)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Error("activeDeadlineSeconds missing: a hung tool would never release its Lease")
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished <= 0 {
		t.Error("TTL missing: finished Jobs would pile up")
	}
}

func TestNameIsDeterministicIdempotencyKey(t *testing.T) {
	c := testConfig()
	a := c.Name(testRequest("metallb-acme", "gen1-rev-abc"))
	b := c.Name(testRequest("metallb-acme", "gen1-rev-abc"))
	if a != b {
		t.Errorf("same intent => different names: %q vs %q", a, b)
	}
	if c := c.Name(testRequest("metallb-acme", "gen2-rev-abc")); c == a {
		t.Errorf("different intents => same name %q", c)
	}
	req := testRequest("metallb-acme", "gen1-rev-abc")
	req.Verb = VerbHelmUninstall
	if u := c.Name(req); u == a {
		t.Errorf("different verbs => same name %q", u)
	}
}

func TestNameStaysWithinDNSLimits(t *testing.T) {
	c := testConfig()
	long := strings.Repeat("a", 200)
	for _, name := range []string{"x", "metallb-acme", long} {
		got := c.Name(testRequest(name, "fp"))
		if len(got) > maxNameLen {
			t.Errorf("name of %d characters for an owner of %d: %q", len(got), len(name), got)
		}
		if !strings.HasPrefix(got, "enrollment-") {
			t.Errorf("the kind must survive truncation: %q", got)
		}
		if !strings.Contains(got, "helmapply") {
			t.Errorf("the verb must survive truncation: %q", got)
		}
	}
	a := c.Name(testRequest(long+"-one", "fp-one"))
	b := c.Name(testRequest(long+"-two", "fp-two"))
	if a == b {
		t.Errorf("truncated names collide: %q", a)
	}
}

func TestBuildSetsControllerOwnerReference(t *testing.T) {
	job, err := testConfig().Build(testRequest("metallb-acme", "fp"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("%d ownerReferences, want 1", len(job.OwnerReferences))
	}
	ref := job.OwnerReferences[0]
	if ref.Kind != "Enrollment" || ref.Name != "metallb-acme" {
		t.Errorf("ownerRef = %s/%s, want Enrollment/metallb-acme", ref.Kind, ref.Name)
	}
	if ref.UID == "" {
		t.Error("ownerRef without UID: the GC would not link the Job")
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Error("ownerRef.controller must be true")
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Error("ownerRef.blockOwnerDeletion must be true (the Job ends before the CR)")
	}
}

func TestBuildPassesReferenceNotSpec(t *testing.T) {
	job, err := testConfig().Build(testRequest("metallb-acme", "fp"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctr := job.Spec.Template.Spec.Containers[0]
	env := map[string]string{}
	for _, e := range ctr.Env {
		env[e.Name] = e.Value
	}
	if got := env["RONDO_TARGET_REF"]; got != "rondo-system/metallb-acme" {
		t.Errorf("RONDO_TARGET_REF = %q, want rondo-system/metallb-acme", got)
	}
	if got := env["RONDO_TARGET_KIND"]; got != "Enrollment" {
		t.Errorf("RONDO_TARGET_KIND = %q, want Enrollment", got)
	}
	if want := []string{"exec", "helm-apply"}; strings.Join(ctr.Args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", ctr.Args, want)
	}
}

func TestBuildHardensThePod(t *testing.T) {
	job, err := testConfig().Build(testRequest("r", "fp"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	pod := job.Spec.Template.Spec
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Error("runAsNonRoot expected")
	}
	sc := pod.Containers[0].SecurityContext
	if sc == nil {
		t.Fatal("container securityContext missing")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation must be false")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("readOnlyRootFilesystem must be true")
	}
	if len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
		t.Error("all capabilities must be dropped")
	}
	var hasWork bool
	for _, m := range pod.Containers[0].VolumeMounts {
		if m.MountPath == "/work" {
			hasWork = true
		}
	}
	if !hasWork {
		t.Error("writable /work expected (read-only rootfs)")
	}
	env := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		env[e.Name] = e.Value
	}
	for _, k := range []string{"HOME", "HELM_CACHE_HOME", "TF_DATA_DIR"} {
		if !strings.HasPrefix(env[k], "/work") {
			t.Errorf("%s = %q, must point under /work (read-only rootfs)", k, env[k])
		}
	}
}

func TestBuildMountsTheAirgapTrust(t *testing.T) {
	job, err := testConfig().Build(testRequest("r", "fp"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctr := job.Spec.Template.Spec.Containers[0]
	var caMounted bool
	for _, m := range ctr.VolumeMounts {
		if m.Name == "mirror-ca" {
			caMounted = true
			if !m.ReadOnly {
				t.Error("the CA must be mounted read-only")
			}
		}
	}
	if !caMounted {
		t.Error("mirror CA not mounted")
	}
	var sslCertFile string
	for _, e := range ctr.Env {
		if e.Name == "SSL_CERT_FILE" {
			sslCertFile = e.Value
		}
	}
	if sslCertFile == "" {
		t.Error("SSL_CERT_FILE expected: it is what Go binaries (helm, tofu) honor")
	}
	var credsFrom bool
	for _, f := range ctr.EnvFrom {
		if f.SecretRef != nil && f.SecretRef.Name == "rondo-credentials" {
			credsFrom = true
		}
	}
	if !credsFrom {
		t.Error("mirror credentials not exposed")
	}
	for _, e := range ctr.Env {
		if e.Value != "" && (strings.Contains(strings.ToLower(e.Name), "password") ||
			strings.Contains(strings.ToLower(e.Name), "token")) {
			t.Errorf("plaintext secret in the env: %s", e.Name)
		}
	}
}

func TestBuildRejectsIncompleteRequests(t *testing.T) {
	c := testConfig()
	cases := map[string]Request{
		"no owner":       {Verb: VerbHelmApply, OwnerGVK: releaseGVK, Fingerprint: "fp"},
		"no kind":        {Verb: VerbHelmApply, Owner: testRelease("r"), Fingerprint: "fp"},
		"no verb":        {Owner: testRelease("r"), OwnerGVK: releaseGVK, Fingerprint: "fp"},
		"no fingerprint": {Verb: VerbHelmApply, Owner: testRelease("r"), OwnerGVK: releaseGVK},
	}
	for name, req := range cases {
		if _, err := c.Build(req); err == nil {
			t.Errorf("%s: Build succeeded, want an error", name)
		}
	}
	noImage := testConfig()
	noImage.Image = ""
	if _, err := noImage.Build(testRequest("r", "fp")); err == nil {
		t.Error("image missing: Build succeeded, want an error")
	}
}

func TestVerbMutating(t *testing.T) {
	for verb, want := range map[Verb]bool{
		VerbHelmApply:     true,
		VerbHelmUninstall: true,
		VerbVMApply:       true,
		VerbVMDestroy:     true,
		VerbVMInstall:     true,
		VerbVMPlan:        false,
	} {
		if got := verb.Mutating(); got != want {
			t.Errorf("%s.Mutating() = %v, want %v", verb, got, want)
		}
	}
}

func TestTimeoutOverride(t *testing.T) {
	req := testRequest("r", "fp")
	req.Timeout = 45 * time.Minute
	job, err := testConfig().Build(req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := *job.Spec.ActiveDeadlineSeconds; got != 2700 {
		t.Errorf("activeDeadlineSeconds = %d, want 2700", got)
	}
	job, err = testConfig().Build(testRequest("r", "fp"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := *job.Spec.ActiveDeadlineSeconds; got != 600 {
		t.Errorf("default activeDeadlineSeconds = %d, want 600", got)
	}
}

func TestFinishedAndFailureMessage(t *testing.T) {
	running := &batchv1.Job{}
	if done, _ := Finished(running); done {
		t.Error("a Job without conditions is running, not finished")
	}

	ok := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	}}}
	if done, succeeded := Finished(ok); !done || !succeeded {
		t.Errorf("Complete => (true,true), got (%v,%v)", done, succeeded)
	}

	ko := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
			Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"},
	}}}
	if done, succeeded := Finished(ko); !done || succeeded {
		t.Errorf("Failed => (true,false), got (%v,%v)", done, succeeded)
	}
	if msg := FailureMessage(ko); !strings.Contains(msg, "DeadlineExceeded") {
		t.Errorf("failure message = %q, must carry the reason", msg)
	}
	bare := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
	}}}
	if msg := FailureMessage(bare); msg == "" {
		t.Error("empty failure message: the operator would have no lead")
	}
	falseCond := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionFalse},
	}}}
	if done, _ := Finished(falseCond); done {
		t.Error("False condition read as finished")
	}
}

func TestJobDeclaresResourceBounds(t *testing.T) {
	job, err := testConfig().Build(testRequest("metallb-acme", "gen1"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := job.Spec.Template.Spec.Containers[0].Resources

	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		req := res.Requests[name]
		if req.IsZero() {
			t.Errorf("requests.%s missing: the Job is BestEffort, so rejected by a "+
				"LimitRange/ResourceQuota on the namespace", name)
		}
		lim := res.Limits[name]
		if lim.IsZero() {
			t.Errorf("limits.%s missing: a bloating tool can evict the controller", name)
		}
		if !req.IsZero() && !lim.IsZero() && lim.Cmp(req) < 0 {
			t.Errorf("limits.%s (%s) < requests.%s (%s): rejected by the apiserver",
				name, lim.String(), name, req.String())
		}
	}

	const floor = "1Gi"
	memFloor := resource.MustParse(floor)
	if lim := res.Limits[corev1.ResourceMemory]; !lim.IsZero() && lim.Cmp(memFloor) < 0 {
		t.Errorf("limits.memory = %s, below the %s floor: an OOM-killed tofu apply "+
			"leaves a diverging state", lim.String(), floor)
	}
}

func TestExplicitBoundsAreHonored(t *testing.T) {
	c := testConfig()
	c.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
	}
	job, err := c.Build(testRequest("metallb-acme", "gen1"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := job.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if got.String() != "4Gi" {
		t.Errorf("limits.memory = %s, want 4Gi (the default overwrote the site setting)", got.String())
	}
}

func TestOnAReadOnlyRootfsEveryWriteIsMounted(t *testing.T) {
	for _, verb := range []Verb{VerbVMPlan, VerbVMApply, VerbHelmApply, VerbVMInstall} {
		t.Run(string(verb), func(t *testing.T) {
			req := testRequest("r", "fp")
			req.Verb = verb
			job, err := testConfig().Build(req)
			if err != nil {
				t.Fatalf("Build(%s): %v", verb, err)
			}
			c := job.Spec.Template.Spec.Containers[0]
			sc := c.SecurityContext
			if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
				t.Fatal("the rootfs must stay read-only; if this hardening goes, " +
					"this test protects nothing and that must be said, not the test deleted")
			}

			covered := func(p string) (writable, known bool) {
				best := ""
				for _, m := range c.VolumeMounts {
					if p == m.MountPath || strings.HasPrefix(p, strings.TrimSuffix(m.MountPath, "/")+"/") {
						if len(m.MountPath) > len(best) {
							best, writable, known = m.MountPath, !m.ReadOnly, true
						}
					}
				}
				return writable, known
			}

			if w, known := covered("/tmp"); !known || !w {
				t.Errorf("/tmp is covered by no writable mount: os.CreateTemp "+
					"will write to the read-only rootfs (covered=%v writable=%v). "+
					"This is the 2026-08-12 outage on `tofu init`", known, w)
			}

			for _, e := range c.Env {
				if !strings.HasPrefix(e.Value, "/") || e.ValueFrom != nil {
					continue
				}
				if _, known := covered(e.Value); !known {
					t.Errorf("%s=%s: absolute path covered by NO mount. On a read-only "+
						"rootfs it can only be read from the image, and no tool cache "+
						"lives there. Mount it, writable if it is written to",
						e.Name, e.Value)
				}
			}
		})
	}
}
