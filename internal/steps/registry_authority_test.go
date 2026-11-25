package steps

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"rondo/internal/config"
	"rondo/internal/kobj"
)

func TestClusterRegistryIsAnAuthorityAndHonoursTheOverride(t *testing.T) {
	cfg := siteCfg()
	r := &config.RKE2Target{}
	if got := cfg.RegistryHostFor(r); got != "rondo-docker-local.artifactory.example.local" {
		t.Errorf("default registry = %q: RKE2 accepts no path in "+
			"system-default-registry, it needs the repository subdomain", got)
	}
	r.RegistryHost = "registry.site.local"
	if got := cfg.RegistryHostFor(r); got != "registry.site.local" {
		t.Errorf("override ignored: %q", got)
	}
	if got := cfg.ManagementImageRegistry(); got != "artifactory.example.local/rondo-docker-local" {
		t.Errorf("management registry = %q, want the path mode", got)
	}
}

func TestDefaultClassFollowsTheChartThatCreatesIt(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "apps", "local-path-provisioner",
		"values.yaml"))
	if err != nil {
		t.Fatalf("local-path-provisioner chart values.yaml unreadable: %v, the "+
			"guard cannot check the constant against the chart", err)
	}
	var vals struct {
		StorageClass struct {
			Name    string `yaml:"name"`
			Default bool   `yaml:"default"`
		} `yaml:"storageClass"`
	}
	if err := yaml.Unmarshal(raw, &vals); err != nil {
		t.Fatalf("values.yaml unreadable: %v", err)
	}
	if vals.StorageClass.Name == "" {
		t.Fatal("the chart names no class: the rest of the test is meaningless")
	}
	if vals.StorageClass.Name != kobj.DefaultStorageClass {
		t.Errorf("the chart creates class %q but kobj.DefaultStorageClass is %q: "+
			"a site without override would see its PVCs name a NONEXISTENT class, hence "+
			"Pending forever with a perfectly plausible name in the status",
			vals.StorageClass.Name, kobj.DefaultStorageClass)
	}
	if !vals.StorageClass.Default {
		t.Errorf("the chart does not mark %q as default: charts that OMIT "+
			"storageClassName (Grafana, VictoriaLogs) would get no class at all",
			vals.StorageClass.Name)
	}
}
