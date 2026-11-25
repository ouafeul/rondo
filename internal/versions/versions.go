package versions

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

type entry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256,omitempty"`
}

// toolOf is the workstation tool each binary dependency installs (internal/tools).
var toolOf = map[string]string{
	"opentofu": "tofu", "helm": "helm", "helmfile": "helmfile", "helm-diff": "helm-diff",
	"kubectl": "kubectl", "k9s": "k9s", "yq": "yq",
}

// ToolDigests are the sha256 that dependencies.yaml pins for the workstation tools, by tool
// name, each for the version next to it.
func ToolDigests(dependenciesPath string) (map[string]string, error) {
	raw, err := os.ReadFile(dependenciesPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dependenciesPath, err)
	}
	var m manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", dependenciesPath, err)
	}
	out := map[string]string{}
	for _, b := range m.Binaries {
		if tool, ok := toolOf[b.Name]; ok && b.SHA256 != "" {
			out[tool] = strings.ToLower(strings.TrimSpace(b.SHA256))
		}
	}
	return out, nil
}

type manifest struct {
	Binaries       []entry `json:"binaries"`
	HelmCharts     []entry `json:"helm_charts"`
	GrafanaPlugins []entry `json:"grafana_plugins"`
}

func lookup(es []entry, name string) string {
	for _, e := range es {
		if e.Name == name {
			return e.Version
		}
	}
	return ""
}

func Env(dependenciesPath string) (map[string]string, error) {
	raw, err := os.ReadFile(dependenciesPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dependenciesPath, err)
	}
	var m manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", dependenciesPath, err)
	}
	bin := func(n string) string { return lookup(m.Binaries, n) }
	chart := func(n string) string { return lookup(m.HelmCharts, n) }
	plugin := func(n string) string { return lookup(m.GrafanaPlugins, n) }

	e := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			e[k] = v
		}
	}
	set("TOFU_VERSION", bin("opentofu"))
	set("HELM_VERSION", bin("helm"))
	set("HELMFILE_VERSION", bin("helmfile"))
	set("HELM_DIFF_VERSION", bin("helm-diff"))
	set("KUBECTL_VERSION", bin("kubectl"))
	set("K9S_VERSION", bin("k9s"))
	set("YQ_VERSION", bin("yq"))
	set("RKE2_VERSION", bin("rke2"))
	set("METALLB_VERSION", chart("metallb"))
	set("CERT_MANAGER_VERSION", chart("cert-manager"))
	set("TRAEFIK_VERSION", chart("traefik"))
	set("KUBE_PROMETHEUS_STACK_VERSION", chart("kube-prometheus-stack"))
	set("VICTORIA_LOGS_VERSION", chart("victoria-logs-single"))
	set("FLUENT_BIT_VERSION", chart("fluent-bit"))
	set("RANCHER_VERSION", chart("rancher"))
	set("VICTORIALOGS_PLUGIN_VERSION", plugin("victoriametrics-logs-datasource"))
	return e, nil
}
