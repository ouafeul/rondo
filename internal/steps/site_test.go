package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"rondo"
	"rondo/internal/config"
	"rondo/internal/kobj"
	"rondo/internal/runner"
)

type applied struct {
	objs []map[string]any
}

func capture(t *testing.T, cfg *config.Config) (*PlatformStage, *applied, *runner.Fake) {
	t.Helper()
	proj := t.TempDir()
	caPath := filepath.Join(proj, "artifactory-ca.pem")
	if err := os.WriteFile(caPath, []byte("-----BEGIN CERTIFICATE-----\narti\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{ProjectDir: proj, StateDir: proj, BinDir: filepath.Join(proj, "bin"), CACert: caPath}

	got := &applied{}
	fk := &runner.Fake{Outputs: map[string]string{}, Errs: map[string]error{}}
	fk.Effect = func(c runner.Cmd) error {
		if c.Stdin == "" {
			return nil
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(c.Stdin), &obj); err != nil {
			t.Errorf("applied manifest unreadable: %v\n%s", err, c.Stdin)
			return nil
		}
		got.objs = append(got.objs, obj)
		return nil
	}
	kube := NewKubeClient(ws, fk)
	p := NewPlatformStage(cfg, ws, kube, nil, "v0.9.0", false)
	return p, got, fk
}

func (a *applied) find(t *testing.T, kind, name string) map[string]any {
	t.Helper()
	for _, o := range a.objs {
		k, _ := o["kind"].(string)
		meta, _ := o["metadata"].(map[string]any)
		n, _ := meta["name"].(string)
		if k == kind && n == name {
			return o
		}
	}
	t.Fatalf("no %s/%s applied; objects = %s", kind, name, a.names())
	return nil
}

func (a *applied) names() string {
	var out []string
	for _, o := range a.objs {
		k, _ := o["kind"].(string)
		meta, _ := o["metadata"].(map[string]any)
		n, _ := meta["name"].(string)
		out = append(out, k+"/"+n)
	}
	return strings.Join(out, ", ")
}

func nested(t *testing.T, obj map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := obj
	for _, k := range path {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("path %v missing (stuck at %q)", path, k)
		}
		cur = next
	}
	return cur
}

// siteCfg is the example manifest on an Artifactory, which mints the mirror tokens.
func siteCfg() *config.Config {
	raw, err := rondo.ExampleConfigFor(config.PresetArtifactory)
	if err != nil {
		panic(err)
	}
	return withSiteExtras(raw)
}

// genericCfg is the example manifest on a generic mirror.
func genericCfg() *config.Config {
	return withSiteExtras(rondo.ExampleConfig)
}

func withSiteExtras(raw []byte) *config.Config {
	c, err := config.Parse(raw)
	if err != nil {
		panic(err)
	}
	c.Targets.VSphere[0].Roles = []string{"dmz"}
	c.Targets.VSphere[0].ComponentValues = map[string]string{"vmIPs.demo-mft.production": `["10.0.2.10"]`}
	c.Secrets.Components = map[string]map[string]string{
		"demo-mft-secrets": {"NC_DB_PASSWORD": "dbpw", "NC_ADMIN_PASSWORD": "ncpw"},
	}
	return c
}

func TestApplySiteProjectsEverything(t *testing.T) {
	p, got, fk := capture(t, siteCfg())
	fk.Effect = chain(fk.Effect, func(c runner.Cmd) error {
		if c.Name != "ssh-keygen" {
			return nil
		}
		keyPath := c.Args[len(c.Args)-1]
		if err := os.WriteFile(keyPath, []byte("PRIVATE"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 AAAA rondo-vm\n"), 0o600)
	})

	if err := p.ApplySite(t.Context()); err != nil {
		t.Fatalf("ApplySite: %v", err)
	}

	mgmt := got.find(t, "Target", "management")
	if roles, _ := nested(t, mgmt, "spec")["roles"].([]any); len(roles) != 1 || roles[0] != "management" {
		t.Errorf("management roles = %v", roles)
	}
	if k := nested(t, mgmt, "spec", "kubernetes"); k["inCluster"] != true {
		t.Errorf("management inCluster = %v", k["inCluster"])
	}
	values := nested(t, mgmt, "spec", "values")
	if values["ingressIP"] != "10.0.0.100" || values["storageClass"] != "local-path" ||
		values["imageRegistry"] != "artifactory.example.local/rondo-docker-local" {
		t.Errorf("management values = %v", values)
	}

	dc1 := got.find(t, "Target", "dc1")
	if roles, _ := nested(t, dc1, "spec")["roles"].([]any); len(roles) != 1 || roles[0] != "dmz" {
		t.Errorf("dc1 roles = %v", roles)
	}
	vs := nested(t, dc1, "spec", "vsphere")
	if nested(t, vs, "settingsConfigMapRef")["name"] != "rondo-vsphere-dc1" ||
		nested(t, vs, "credentialsSecretRef")["name"] != "rondo-vsphere-dc1" {
		t.Errorf("placement vSphere = %v", vs)
	}
	zvals := nested(t, dc1, "spec", "values")
	if zvals["vmTemplate"] != "rhel-9-template-cloudinit" || zvals["sshUser"] != kobj.VMSSHUser ||
		zvals["vmIPs.demo-mft.production"] != `["10.0.2.10"]` {
		t.Errorf("dc1 values = %v", zvals)
	}

	data := nested(t, got.find(t, "ConfigMap", "rondo-vsphere-dc1"), "data")
	for k, want := range map[string]string{
		"vsphere_server":     "vcenter.example.local",
		"vsphere_datacenter": "DC1",
		"vsphere_folder":     "rondo/prod",
		"network_gateway":    "10.0.0.1",
		"network_netmask":    "24",
		"network_dns":        `["10.0.0.2"]`,
	} {
		if data[k] != want {
			t.Errorf("%s = %v, want %q", k, data[k], want)
		}
	}
	if _, leaked := data["vsphere_password"]; leaked {
		t.Error("the vCenter password is in the ConfigMap")
	}
	if !strings.Contains(data["mirror_ca_pem"].(string), "BEGIN CERTIFICATE") {
		t.Errorf("Artifactory CA not projected: %v", data["mirror_ca_pem"])
	}
	creds := nested(t, got.find(t, "Secret", "rondo-vsphere-dc1"), "stringData")
	if creds["vsphere_user"] != "administrator@vsphere.local" || creds["vsphere_password"] != "CHANGEME" {
		t.Errorf("identifiants vSphere = %v", creds)
	}

	sd := nested(t, got.find(t, "Secret", kobj.PlatformValuesSecret), "stringData")
	if sd["artifactoryPassword"] != "CHANGEME" || sd["artifactoryUser"] != "svc-rondo" {
		t.Errorf("secret values = %v", sd)
	}

	ksd := nested(t, got.find(t, "Secret", kobj.VMSSHSecret), "stringData")
	if ksd["ssh-privatekey"] != "PRIVATE" || !strings.HasPrefix(ksd["ssh-publickey"].(string), "ssh-ed25519") {
		t.Errorf("key pair = %v", ksd)
	}
}

func TestApplyRKE2TargetUsesItsKubeconfig(t *testing.T) {
	cfg := siteCfg()
	p, got, _ := capture(t, cfg)
	rke2 := cfg.Targets.Downstreams[0]
	if err := p.ApplyRKE2Target(t.Context(), &rke2, "https://10.0.0.31:6443", "apiVersion: v1\n"); err != nil {
		t.Fatalf("ApplyRKE2Target: %v", err)
	}
	if nested(t, got.find(t, "Secret", "rondo-kubeconfig-rke2-a"), "stringData")["kubeconfig"] != "apiVersion: v1\n" {
		t.Error("kubeconfig not projected")
	}
	tg := got.find(t, "Target", "rke2-a")
	if roles, _ := nested(t, tg, "spec")["roles"].([]any); len(roles) != 1 || roles[0] != "monitoring" {
		t.Errorf("rke2-a roles = %v", roles)
	}
	k := nested(t, tg, "spec", "kubernetes")
	if k["server"] != "https://10.0.0.31:6443" || nested(t, k, "kubeconfigSecretRef")["name"] != "rondo-kubeconfig-rke2-a" {
		t.Errorf("access = %v", k)
	}
	v := nested(t, tg, "spec", "values")
	if v["ingressIP"] != "10.0.0.50" || v["imageRegistry"] != "rondo-docker-local.artifactory.example.local" ||
		v["storageClass"] != "local-path" {
		t.Errorf("valeurs = %v", v)
	}
}

func TestApplySiteProjectsComponentSecrets(t *testing.T) {
	p, got, _ := capture(t, siteCfg())
	if err := p.applyComponentSecrets(t.Context()); err != nil {
		t.Fatalf("applyComponentSecrets: %v", err)
	}
	secret := got.find(t, "Secret", "demo-mft-secrets")
	if secret["type"] != "Opaque" {
		t.Errorf("type = %v", secret["type"])
	}
	if ns := nested(t, secret, "metadata")["namespace"]; ns != "rondo-system" {
		t.Errorf("namespace = %v: the component consumes it from the platform "+
			"namespace", ns)
	}
	sd := nested(t, secret, "stringData")
	if sd["NC_DB_PASSWORD"] != "dbpw" || sd["NC_ADMIN_PASSWORD"] != "ncpw" {
		t.Errorf("data = %v", sd)
	}
}

func TestApplyComponentSecretsAppliesNothingWithoutDeclaration(t *testing.T) {
	cfg := siteCfg()
	cfg.Secrets.Components = nil
	p, got, _ := capture(t, cfg)
	if err := p.applyComponentSecrets(t.Context()); err != nil {
		t.Fatalf("applyComponentSecrets: %v", err)
	}
	if len(got.objs) != 0 {
		t.Errorf("objects applied without a declaration: %s", got.names())
	}
}

func TestEnsureVMSSHKeyNeverRotates(t *testing.T) {
	p, got, fk := capture(t, siteCfg())
	fk.Outputs[filepath.Join(p.ws.BinDir, "kubectl")+" get secret "+kobj.VMSSHSecret+
		" -n rondo-system -o jsonpath={.data.ssh-publickey}"] = "c3NoLWVkMjU1MTk="

	if err := p.ensureVMSSHKey(t.Context()); err != nil {
		t.Fatalf("ensureVMSSHKey: %v", err)
	}
	for _, c := range fk.Calls {
		if c.Name == "ssh-keygen" {
			t.Fatal("key REGENERATED although it exists: already provisioned machines " +
				"would become unreachable")
		}
	}
	if len(got.objs) != 0 {
		t.Errorf("objects applied although there was nothing to do: %s", got.names())
	}
}

func TestApplySiteFailsWithoutArtifactoryCA(t *testing.T) {
	p, _, _ := capture(t, siteCfg())
	p.ws.CACert = filepath.Join(t.TempDir(), "missing.pem")
	err := p.ApplySite(t.Context())
	if err == nil {
		t.Fatal("missing CA accepted")
	}
	if !strings.Contains(err.Error(), "Artifactory CA") {
		t.Errorf("message = %q", err)
	}
}

func TestApplySiteDryRunAppliesNothing(t *testing.T) {
	proj := t.TempDir()
	fk := &runner.Fake{Outputs: map[string]string{}, Errs: map[string]error{}}
	ws := Workspace{ProjectDir: proj, StateDir: proj, BinDir: "bin", CACert: filepath.Join(proj, "ca.pem")}
	p := NewPlatformStage(siteCfg(), ws, NewKubeClient(ws, fk), nil, "v0.9.0", true)
	for _, fn := range []func() error{
		func() error { return p.ApplySite(t.Context()) },
		func() error { return p.ApplyCredentials(t.Context()) },
		func() error { return p.ApplyMirrorToken(t.Context()) },
	} {
		if err := fn(); err != nil {
			t.Fatalf("dry-run failed: %v", err)
		}
	}
	if len(fk.Calls) != 0 {
		t.Errorf("%d commands run in dry-run: %v", len(fk.Calls), fk.Calls)
	}
}

func TestApplyCredentialsKeysAreEnvNames(t *testing.T) {
	p, got, _ := capture(t, siteCfg())
	if err := p.ApplyCredentials(t.Context()); err != nil {
		t.Fatalf("ApplyCredentials: %v", err)
	}
	creds := got.find(t, "Secret", "rondo-credentials")
	sd := nested(t, creds, "stringData")
	for _, k := range []string{
		"ARTIFACTORY_USER", "ARTIFACTORY_PASSWORD",
	} {
		if sd[k] == nil || sd[k] == "" {
			t.Errorf("key %q missing or empty: the Jobs would receive an empty variable", k)
		}
	}
	ca := got.find(t, "ConfigMap", "artifactory-ca")
	if !strings.Contains(nested(t, ca, "data")["ca.pem"].(string), "BEGIN CERTIFICATE") {
		t.Error("CA not projected")
	}
}

type recordingMinter struct {
	next    string
	minted  []string
	revoked []string
	mintErr error
}

func (m *recordingMinter) MintDurableToken(context.Context) (string, error) {
	if m.mintErr != nil {
		return "", m.mintErr
	}
	m.minted = append(m.minted, m.next)
	return m.next, nil
}

func (m *recordingMinter) RevokeToken(_ context.Context, token string) error {
	m.revoked = append(m.revoked, token)
	return nil
}

func TestApplyMirrorTokenRotates(t *testing.T) {
	p, got, fk := capture(t, siteCfg())
	m := &recordingMinter{next: "tok-new"}
	p.arti = m
	fk.Outputs[filepath.Join(p.ws.BinDir, "kubectl")+
		" get secret rondo-tofurc -n rondo-system -o jsonpath={.data.token}"] = "dG9rLW9sZA=="

	if err := p.ApplyMirrorToken(t.Context()); err != nil {
		t.Fatalf("ApplyMirrorToken: %v", err)
	}
	sec := got.find(t, "Secret", "rondo-tofurc")
	tofurc, _ := nested(t, sec, "stringData")[".tofurc"].(string)
	if !strings.Contains(tofurc, "network_mirror") {
		t.Errorf(".tofurc = %q", tofurc)
	}
	if !strings.Contains(tofurc, "tok-new") {
		t.Errorf(".tofurc does not carry the minted token: %q", tofurc)
	}
	if nested(t, sec, "stringData")["token"] != "tok-new" {
		t.Error("token not persisted: the next deploy could not revoke it")
	}
	if len(m.revoked) != 1 || m.revoked[0] != "tok-old" {
		t.Errorf("revocations = %v, want the previous token", m.revoked)
	}
}

func TestApplyMirrorTokenOverrideIsNotPersisted(t *testing.T) {
	cfg := siteCfg()
	cfg.Secrets.MirrorToken = "  tok-manual  "
	p, got, _ := capture(t, cfg)
	m := &recordingMinter{}
	p.arti = m

	if err := p.ApplyMirrorToken(t.Context()); err != nil {
		t.Fatalf("ApplyMirrorToken: %v", err)
	}
	if len(m.minted) != 0 {
		t.Errorf("a token was minted despite the override: %v", m.minted)
	}
	sd := nested(t, got.find(t, "Secret", "rondo-tofurc"), "stringData")
	if _, persisted := sd["token"]; persisted {
		t.Error("the operator-provided token was persisted, it would be revoked by the next deploy")
	}
	if !strings.Contains(sd[".tofurc"].(string), "tok-manual") {
		t.Errorf(".tofurc = %v", sd[".tofurc"])
	}
}

func TestApplyMirrorTokenRefusesWithoutMinter(t *testing.T) {
	p, _, _ := capture(t, siteCfg())
	p.arti = nil
	if err := p.ApplyMirrorToken(t.Context()); err == nil {
		t.Fatal("no minter and no override accepted")
	}
}

func TestApplyMirrorTokenOnAGenericMirror(t *testing.T) {
	for name, tc := range map[string]struct {
		token string
		want  string
	}{
		"anonymous":      {"", ""},
		"declared token": {"tok-site", `credentials "files.example.local" {` + "\n" + `  token = "tok-site"`},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := genericCfg()
			cfg.Secrets.MirrorToken = tc.token
			p, got, _ := capture(t, cfg)
			p.arti = nil
			if err := p.ApplyMirrorToken(t.Context()); err != nil {
				t.Fatalf("ApplyMirrorToken: %v", err)
			}
			sd := nested(t, got.find(t, "Secret", "rondo-tofurc"), "stringData")
			tofurc, _ := sd[".tofurc"].(string)
			if !strings.Contains(tofurc, `url = "https://files.example.local/tofu-providers/"`) {
				t.Errorf(".tofurc does not point at artifacts.tofu.providerMirror: %q", tofurc)
			}
			if tc.want == "" && strings.Contains(tofurc, "credentials") {
				t.Errorf("anonymous mirror given credentials: %q", tofurc)
			}
			if !strings.Contains(tofurc, tc.want) {
				t.Errorf(".tofurc = %q, want %q", tofurc, tc.want)
			}
			if _, persisted := sd["token"]; persisted {
				t.Error("a token RONDO did not mint was persisted, the next deploy would revoke it")
			}
		})
	}
}

// A generic mirror never reaches the JFrog token API, a minter wired and a token kept or not.
func TestApplyMirrorTokenNeverMintsNorRevokesOnAGenericMirror(t *testing.T) {
	p, got, fk := capture(t, genericCfg())
	m := &recordingMinter{next: "tok"}
	p.arti = m
	fk.Outputs[filepath.Join(p.ws.BinDir, "kubectl")+
		" get secret rondo-tofurc -n rondo-system -o jsonpath={.data.token}"] = "dG9rLW9sZA=="
	if err := p.ApplyMirrorToken(t.Context()); err != nil {
		t.Fatalf("ApplyMirrorToken: %v", err)
	}
	if len(m.minted) != 0 || len(m.revoked) != 0 {
		t.Errorf("minted %v, revoked %v on a generic mirror", m.minted, m.revoked)
	}
	if _, kept := nested(t, got.find(t, "Secret", "rondo-tofurc"), "stringData")["token"]; kept {
		t.Error("a token kept for revocation on a generic mirror")
	}
}

func TestApplyMirrorTokenRevokesOrphan(t *testing.T) {
	p, _, fk := capture(t, siteCfg())
	m := &recordingMinter{next: "tok-orphan"}
	p.arti = m
	fk.Errs[filepath.Join(p.ws.BinDir, "kubectl")+" apply -f -"] = errApply{}

	if err := p.ApplyMirrorToken(t.Context()); err == nil {
		t.Fatal("write failure swallowed")
	}
	if len(m.revoked) != 1 || m.revoked[0] != "tok-orphan" {
		t.Errorf("revocations = %v, the orphan token must be revoked", m.revoked)
	}
}

type errApply struct{}

func (errApply) Error() string { return "error: unable to recognize \"STDIN\"" }

func TestDescribeObject(t *testing.T) {
	if got := describeObject(map[string]any{
		"kind": "ConfigMap", "metadata": map[string]any{"name": "x"},
	}); got != "ConfigMap/x" {
		t.Errorf("describeObject = %q", got)
	}
	if got := describeObject(map[string]any{}); got == "" {
		t.Error("describeObject of an empty object = empty string")
	}
}

func chain(a, b func(runner.Cmd) error) func(runner.Cmd) error {
	return func(c runner.Cmd) error {
		if a != nil {
			if err := a(c); err != nil {
				return err
			}
		}
		return b(c)
	}
}
