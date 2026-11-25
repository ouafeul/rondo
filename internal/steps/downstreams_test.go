package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/kobj"
	"rondo/internal/runner"
)

func TestApplyRKE2ProvisionsWithItsOwnStateAndVariables(t *testing.T) {
	ws := wsWithCA(t)
	if err := os.MkdirAll(filepath.Dir(ws.RancherCA()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.RancherCA(), []byte("CLUSTER-CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &runner.Fake{Outputs: map[string]string{
		"/bin/tofu show -json tfplan-rke2-a": `{"resource_changes":[{"address":"x","change":{"actions":["create"]}}]}`,
		"/bin/tofu output -raw kubeconfig":   "apiVersion: v1\n",
		"/bin/tofu output -raw api_endpoint": "https://10.0.0.31:6443\n",
	}}
	cfg := siteCfg()
	st := NewTofuStage(cfg, ws, fake, fixedToken("t"), false)
	target := cfg.Targets.Downstreams[0]
	server, kubeconfig, err := st.ApplyRKE2(context.Background(), &target, "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if server != "https://10.0.0.31:6443" || kubeconfig != "apiVersion: v1\n" {
		t.Errorf("sorties = %q / %q", server, kubeconfig)
	}
	init := fake.Calls[0]
	if !strings.Contains(init.String(), "/prod/rke2-a/terraform.tfstate") {
		t.Errorf("state = %q, want one key per target", init.String())
	}
	if init.Dir != filepath.Join(ws.ProjectDir, "terraform", "rke2") {
		t.Errorf("module = %q", init.Dir)
	}
	for k, want := range map[string]string{
		"TF_DATA_DIR":               filepath.Join(ws.StateDir, "tofu", "rke2-a"),
		"TF_VAR_rke2_cluster_name":  "rke2-a",
		"TF_VAR_rancher_access_key": "access",
		"TF_VAR_cluster_ca_pem":     "CLUSTER-CA",
		"TF_VAR_vsphere_password":   "CHANGEME",
	} {
		if init.Env[k] != want {
			t.Errorf("%s = %q, want %q", k, init.Env[k], want)
		}
	}
}

func TestBaseEnrollmentsArePlacedOnTheTarget(t *testing.T) {
	cfg := siteCfg()
	p, got, fk := capture(t, cfg)
	fk.Outputs[p.ws.Tool("kubectl")+" -n rondo-system get configmap "+kobj.RecordName(BaseTreeName("rke2-a"))+
		" --ignore-not-found -o jsonpath={.data.objects}"] = ""
	templates := filepath.Join("..", "..", "platform", "enrollments")
	if err := p.ApplyBaseEnrollments(t.Context(), "rke2-a", templates, "v1"); err != nil {
		t.Fatal(err)
	}
	record := nested(t, got.find(t, "ConfigMap", kobj.RecordName(BaseTreeName("rke2-a"))), "data")
	objects, _ := record["objects"].(string)
	for _, want := range []string{"Enrollment/cert-manager-rke2-a", "Enrollment/downstream-bootstrap-rke2-a"} {
		if !strings.Contains(objects, want) {
			t.Errorf("%s missing from the record: %q", want, objects)
		}
	}
}
