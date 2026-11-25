package bootstrap

import (
	"strings"
	"testing"

	"rondo/internal/config"
)

func TestLesConstructsDeBootstrapSontRendus(t *testing.T) {
	c := &config.Config{IngressBaseDomain: "example.local", Targets: config.Targets{
		Management: config.K3sTarget{IngressLoadBalancerIP: "10.0.0.10"},
	}}
	if got := All(c); len(got) != 2 {
		t.Fatalf("%d bootstrap construct(s), want 2 (PKI + MetalLB pool)", len(got))
	}
}

func TestPlatformInstallForTakesImagesAndDashboard(t *testing.T) {
	c := &config.Config{
		IngressBaseDomain: "example.local",
		Artifactory: config.Artifactory{
			URL:               "https://arti.invalid/artifactory",
			DockerRepository:  "rondo-docker-local",
			GenericRepository: "rondo-generic-local",
		},
	}
	inst := PlatformInstallFor(c, "v1.2.3")
	if !strings.HasSuffix(inst.ControllerImage, ":v1.2.3") {
		t.Errorf("ControllerImage = %q, must end with the version", inst.ControllerImage)
	}
	if !strings.HasSuffix(inst.ToolboxImage, ":v1.2.3") {
		t.Errorf("ToolboxImage = %q, must end with the version", inst.ToolboxImage)
	}
	if inst.DashboardHostname == "" {
		t.Error("empty DashboardHostname for a set IngressBaseDomain")
	}
}
