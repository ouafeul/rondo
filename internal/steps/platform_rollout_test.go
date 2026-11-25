package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/bootstrap"
)

func TestWaitReadyWaitsForTheServicesToStart(t *testing.T) {
	p, _, fk := capture(t, siteCfg())
	crds := filepath.Join(p.ws.ProjectDir, "manifests", "crds")
	if err := os.MkdirAll(crds, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crds, "rondo.ouafeul.github.io_targets.yaml"),
		[]byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := p.installThenWait(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	var want []string
	for _, obj := range bootstrap.PlatformInstallFor(siteCfg(), "v0.9.0").Objects() {
		if kind, _ := obj["kind"].(string); kind != "Deployment" {
			continue
		}
		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		want = append(want, name)
	}
	if len(want) < 3 {
		t.Fatalf("%d Deployments rendered: the operator manifests changed shape", len(want))
	}

	var cmds []string
	for _, c := range fk.Calls {
		cmds = append(cmds, c.String())
	}
	joined := strings.Join(cmds, "\n")
	for _, name := range want {
		if !strings.Contains(joined, "rollout status deployment/"+name) {
			t.Errorf("Install does not wait for %s to start: the rest of the deployment "+
				"would go on before the first image pull ends.\ncommands:\n%s", name, joined)
		}
	}
	if !strings.Contains(joined, "--timeout="+rolloutTimeout.String()) {
		t.Errorf("no wait bounded by %s: a site pull needs this budget",
			rolloutTimeout)
	}
	if rolloutTimeout.Minutes() < 5 {
		t.Errorf("rolloutTimeout = %s: too short for a first image pull over a site link",
			rolloutTimeout)
	}
}

func (p *PlatformStage) installThenWait(ctx context.Context) error {
	if err := p.Install(ctx); err != nil {
		return err
	}
	return p.WaitReady(ctx)
}
