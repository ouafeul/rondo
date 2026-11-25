package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var schemeRe = regexp.MustCompile(`^https?://`)

func (c *Config) ArtifactoryAuthURL() (string, error) {
	scheme, rest, ok := strings.Cut(c.Artifactory.URL, "://")
	if !ok {
		return "", fmt.Errorf("artifactory.url (%q) has no scheme", c.Artifactory.URL)
	}
	return scheme + "://" + escapeUserinfo(c.Artifactory.Username) + ":" +
		escapeUserinfo(c.Secrets.ArtifactoryPassword) + "@" + rest, nil
}

func escapeUserinfo(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		unreserved := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
		if unreserved {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func (c *Config) ArtifactoryHost() string {
	h := schemeRe.ReplaceAllString(c.Artifactory.URL, "")
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	return h
}

func (c *Config) RancherHostname() string   { return "rancher." + c.IngressBaseDomain }
func (c *Config) DashboardHostname() string { return "rondo." + c.IngressBaseDomain }

func (c *Config) ClusterCACommonName() string {
	return strings.TrimSpace("RONDO " + c.EnvironmentName + " CA")
}

func (c *Config) ManagementImageRegistry() string {
	return c.ArtifactoryHost() + "/" + c.Artifactory.DockerRepository
}

func (c *Config) RegistryHostFor(r *RKE2Target) string {
	if r.RegistryHost != "" {
		return r.RegistryHost
	}
	return c.Artifactory.DockerRepository + "." + c.ArtifactoryHost()
}

func (c *Config) StateBaseURL() string {
	return c.Artifactory.URL + "/" + c.Scoped(c.Artifactory.TofuStateRepository)
}

func (c *Config) Scoped(p string) string {
	if c.EnvironmentName == "" {
		return p
	}
	return p + "/" + c.EnvironmentName
}

func (c *Config) StateAddress(name string) string {
	return c.StateBaseURL() + "/" + name + "/terraform.tfstate"
}

func vmFolder(c *Config, v *VSphereTarget) string {
	return strings.TrimPrefix(c.Scoped(v.VMFolder), "/")
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// VSphereVars are the tofu variables placing machines on a vSphere target.
func (c *Config) VSphereVars(v *VSphereTarget) map[string]string {
	return map[string]string{
		"vsphere_server":               v.VCenterAddress,
		"vsphere_user":                 v.VCenterUsername,
		"vsphere_password":             c.Secrets.VCenterPasswords[v.Name],
		"vsphere_allow_unverified_ssl": strconv.FormatBool(v.AllowUnverifiedTLS),
		"vsphere_datacenter":           v.Datacenter,
		"vsphere_cluster":              v.ComputeCluster,
		"vsphere_datastore":            v.Datastore,
		"vsphere_network":              v.VMNetwork.PortGroup,
		"vsphere_template":             v.VMTemplate,
		"vsphere_folder":               vmFolder(c, v),
		"vsphere_resource_pool":        v.ResourcePool,
		"network_gateway":              v.VMNetwork.GatewayIP,
		"network_netmask":              strconv.Itoa(v.VMNetwork.NetmaskBits),
		"network_dns":                  jsonOf(v.VMNetwork.DNSServerIPs),
		"network_domain":               v.VMNetwork.DNSSearchDomain,
	}
}

// RKE2Vars are the variables of terraform/rke2 for one downstream, Rancher access aside.
func (c *Config) RKE2Vars(d *DownstreamTarget) map[string]string {
	r := &d.RKE2
	vars := c.VSphereVars(c.VSphereNamed(r.VSphereTarget))
	for k, v := range map[string]string{
		"rke2_cluster_name":     d.Name,
		"rke2_k8s_version":      r.RKE2Version,
		"rke2_cni":              r.CNIPlugin,
		"rke2_master_ips":       jsonOf(r.ControlPlaneNodeIPs),
		"rke2_master_cpu":       strconv.Itoa(r.ControlPlaneNodeSize.CPUCores),
		"rke2_master_memory_mb": strconv.Itoa(r.ControlPlaneNodeSize.MemoryMB),
		"rke2_master_disk_gb":   strconv.Itoa(r.ControlPlaneNodeSize.DiskGB),
		"rke2_worker_ips":       jsonOf(append([]string{}, r.WorkerNodeIPs...)),
		"rke2_worker_cpu":       strconv.Itoa(r.WorkerNodeSize.CPUCores),
		"rke2_worker_memory_mb": strconv.Itoa(r.WorkerNodeSize.MemoryMB),
		"rke2_worker_disk_gb":   strconv.Itoa(r.WorkerNodeSize.DiskGB),
		"rke2_registry_host":    c.RegistryHostFor(r),
		"rke2_registry_ip":      r.RegistryIP,
		"rancher_api_url":       "https://" + c.RancherHostname(),
		"rancher_ingress_ip":    c.Targets.Management.IngressLoadBalancerIP,
	} {
		vars[k] = v
	}
	return vars
}

func vmsVar(nodes []Node) string {
	vms := make([]map[string]string, len(nodes))
	for i, n := range nodes {
		vms[i] = map[string]string{"name": n.Hostname, "ip": n.IP}
	}
	return jsonOf(vms)
}

// Env is the environment of every tool the CLI runs: platform tokens, helmfile, terraform/k3s.
func (c *Config) Env() map[string]string {
	m := &c.Targets.Management
	vs := c.VSphereNamed(m.VSphereTarget)
	e := map[string]string{
		"ARTIFACTORY_URL":            c.Artifactory.URL,
		"ARTIFACTORY_USER":           c.Artifactory.Username,
		"ARTIFACTORY_PASSWORD":       c.Secrets.ArtifactoryPassword,
		"ARTIFACTORY_GENERIC_REPO":   c.Artifactory.GenericRepository,
		"ARTIFACTORY_RPM_REPO":       c.Artifactory.RPMRepository,
		"ARTIFACTORY_DOCKER_REPO":    c.Artifactory.DockerRepository,
		"ARTIFACTORY_HELM_REPO":      c.Artifactory.HelmRepository,
		"REGISTRY_HOST":              c.ArtifactoryHost(),
		"INGRESS_BASE_DOMAIN":        c.IngressBaseDomain,
		"INGRESS_IP":                 m.IngressLoadBalancerIP,
		"RANCHER_HOSTNAME":           c.RancherHostname(),
		"RANCHER_BOOTSTRAP_PASSWORD": c.Secrets.RancherBootstrapPassword,

		"TF_VAR_artifactory_url":          c.Artifactory.URL,
		"TF_VAR_artifactory_user":         c.Artifactory.Username,
		"TF_VAR_artifactory_pass":         c.Secrets.ArtifactoryPassword,
		"TF_VAR_artifactory_generic_repo": c.Artifactory.GenericRepository,
		"TF_VAR_artifactory_docker_repo":  c.Artifactory.DockerRepository,
		"TF_VAR_environment":              c.EnvironmentName,
		"TF_VAR_vms":                      vmsVar(m.ServerNodes),
		"TF_VAR_vm_specs": jsonOf(map[string]int{"cpu": m.ServerNodeSize.CPUCores,
			"memory_mb": m.ServerNodeSize.MemoryMB, "disk_gb": m.ServerNodeSize.DiskGB}),
		"TF_VAR_ssh_user":     m.NodeSSHUsername,
		"TF_VAR_ssh_password": m.NodeSSHPassword,
		"TF_VAR_k3s_version":  m.K3sVersion,
		"TF_VAR_k3s_token":    c.Secrets.K3sClusterToken,
		"TF_VAR_ingress_ip":   m.IngressLoadBalancerIP,
	}
	for k, v := range c.VSphereVars(vs) {
		e["TF_VAR_"+k] = v
	}
	// terraform/k3s creates <folder>/<environment> itself.
	e["TF_VAR_vsphere_folder"] = strings.TrimPrefix(vs.VMFolder, "/")
	return e
}
