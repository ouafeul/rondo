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
		Artifacts: config.Artifacts{Preset: config.PresetArtifactory, Artifactory: config.ArtifactoryMirror{
			URL: "https://arti.invalid/artifactory",
			Repositories: config.ArtifactoryRepositories{
				Docker: "rondo-docker-local", Generic: "rondo-generic-local", TofuState: "rondo-tofu-state-local",
			},
		}},
	}
	inst := PlatformInstallFor(c, "v1.2.3")
	for got, want := range map[string]string{
		inst.ControllerImage:   "arti.invalid/rondo-docker-local/rondo-controller:v1.2.3",
		inst.ToolboxImage:      "arti.invalid/rondo-docker-local/rondo-toolbox:v1.2.3",
		inst.TofuStateBaseURL:  "https://arti.invalid/artifactory/rondo-tofu-state-local",
		inst.ArtifactoryURL:    "https://arti.invalid/artifactory",
		inst.VMModuleURL:       "https://arti.invalid/artifactory/rondo-generic-local/tofu-modules/vm/v1.2.3/vm.tar.gz",
		inst.AnsibleContentURL: "https://arti.invalid/artifactory/rondo-generic-local/ansible/rondo/v1.2.3/rondo-ansible.tar.gz",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if !strings.HasSuffix(inst.ControllerImage, ":v1.2.3") {
		t.Errorf("ControllerImage = %q, must end with the version", inst.ControllerImage)
	}
	if !strings.HasSuffix(inst.ToolboxImage, ":v1.2.3") {
		t.Errorf("ToolboxImage = %q, must end with the version", inst.ToolboxImage)
	}
	if inst.DashboardHostname == "" {
		t.Error("empty DashboardHostname for a set IngressBaseDomain")
	}
	if inst.TofuStateS3 != "" {
		t.Errorf("TofuStateS3 = %q with an http state", inst.TofuStateS3)
	}
}

func TestPlatformInstallForAnS3State(t *testing.T) {
	c := &config.Config{
		EnvironmentName: "prod",
		Artifacts: config.Artifacts{
			OCI:   config.OCIEndpoint{Registry: "registry.example.local"},
			Files: config.FilesEndpoint{URL: "https://files.example.local"},
			Tofu: config.TofuEndpoints{State: config.TofuState{S3: &config.S3State{
				Bucket: "states", Endpoint: "https://s3.example.local"}}},
		},
	}
	inst := PlatformInstallFor(c, "v1.2.3")
	if inst.TofuStateBaseURL != "" {
		t.Errorf("http state base %q with an s3 state", inst.TofuStateBaseURL)
	}
	want := `{"bucket":"states","endpoint":"https://s3.example.local","region":"us-east-1","keyPrefix":"prod"}`
	if inst.TofuStateS3 != want {
		t.Errorf("TofuStateS3 = %s, want %s", inst.TofuStateS3, want)
	}
}
