package steps

import (
	"context"
	"os"
	"strings"
	"testing"

	"rondo/internal/runner"
)

func TestApplyCoreDNSHosts(t *testing.T) {
	ws := wsWithCA(t)
	if err := os.WriteFile(ws.Kubeconfig(), []byte("kc"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &runner.Fake{}
	p := NewPlatformStage(siteCfg(), ws, NewKubeClient(ws, fake), nil, "0.1.0", false)
	if err := p.ApplyCoreDNSHosts(context.Background()); err != nil {
		t.Fatalf("ApplyCoreDNSHosts: %v", err)
	}
	var body, cmds strings.Builder
	for _, c := range fake.Calls {
		cmds.WriteString(c.String() + "\n")
		if strings.Contains(c.String(), "apply -f -") {
			body.WriteString(c.Stdin)
		}
	}
	s := body.String()
	for _, want := range []string{"coredns-custom", "kube-system", "template IN A",
		`^(rancher|rondo)[.]cluster[.]example[.]local[.]$`, "10.0.0.100",
		`^.+[.](rke2-a|monitoring)[.]cluster[.]example[.]local[.]$`, "10.0.0.50",
		"{{ .Name }}", "fallthrough"} {
		if !strings.Contains(s, want) {
			t.Errorf("coredns-custom: %q missing\n%s", want, s)
		}
	}
	if strings.Contains(s, "hosts {") {
		t.Errorf("must NOT emit a second hosts plugin (conflicts with k3s NodeHosts)\n%s", s)
	}
	if strings.Contains(s, `^.+[.]cluster[.]example`) {
		t.Errorf("must NOT hijack the whole base domain (the mirror may live there)\n%s", s)
	}
	if !strings.Contains(cmds.String(), "rollout restart deployment coredns") {
		t.Errorf("want a CoreDNS restart when the ConfigMap changes\n%s", cmds.String())
	}
}

func TestApplyCoreDNSHostsIdempotent(t *testing.T) {
	ws := wsWithCA(t)
	cfg := siteCfg()
	fake := &runner.Fake{Outputs: map[string]string{
		ws.Tool("kubectl") + ` get configmap coredns-custom -n kube-system -o jsonpath={.data.rondo-ingress-wildcard\.override}`: coreDNSOverride(cfg),
	}}
	p := NewPlatformStage(cfg, ws, NewKubeClient(ws, fake), nil, "0.1.0", false)
	if err := p.ApplyCoreDNSHosts(context.Background()); err != nil {
		t.Fatalf("ApplyCoreDNSHosts: %v", err)
	}
	for _, c := range fake.Calls {
		if strings.Contains(c.String(), "rollout restart") {
			t.Errorf("CoreDNS restarted on an unchanged override: needless DNS blip")
		}
	}
}

func TestCrdNameFromFile(t *testing.T) {
	cases := map[string]string{
		"rondo.ouafeul.github.io_targets.yaml": "targets.rondo.ouafeul.github.io",
		"kustomization.yaml":                   "",
		"README.md":                            "",
	}
	for file, want := range cases {
		if got := crdNameFromFile(file); got != want {
			t.Errorf("crdNameFromFile(%q) = %q, want %q", file, got, want)
		}
	}
}
