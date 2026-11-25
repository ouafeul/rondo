package jobs

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func envOf(c corev1.Container) map[string]string {
	out := map[string]string{}
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

// The state backend reaches the Jobs that run OpenTofu, and those alone.
func TestBuildPassesTheS3StateToTofuVerbs(t *testing.T) {
	cfg := testConfig()
	cfg.TofuStateS3 = `{"bucket":"states"}`
	req := testRequest("mft", "fp")
	req.Verb = VerbVMApply
	job, err := cfg.Build(req)
	if err != nil {
		t.Fatal(err)
	}
	env := envOf(job.Spec.Template.Spec.Containers[0])
	if env["RONDO_TOFU_STATE_S3"] != `{"bucket":"states"}` {
		t.Errorf("RONDO_TOFU_STATE_S3 = %q", env["RONDO_TOFU_STATE_S3"])
	}
	if _, set := env["RONDO_TOFU_STATE_URL"]; set {
		t.Error("an http state URL without one configured")
	}
	helm, err := cfg.Build(testRequest("app", "fp"))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := envOf(helm.Spec.Template.Spec.Containers[0])["RONDO_TOFU_STATE_S3"]; set {
		t.Error("a helm Job gets the state backend")
	}
}
