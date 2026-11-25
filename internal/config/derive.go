package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var schemeRe = regexp.MustCompile(`^https?://`)

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

func (c *Config) RancherHostname() string   { return "rancher." + c.IngressBaseDomain }
func (c *Config) DashboardHostname() string { return "rondo." + c.IngressBaseDomain }

func (c *Config) ClusterCACommonName() string {
	return strings.TrimSpace("RONDO " + c.EnvironmentName + " CA")
}

// RegistryHostFor is the registry authority of an rke2 target: its registryHost, else the mirror's.
func (c *Config) RegistryHostFor(r *RKE2Target) string {
	if r.RegistryHost != "" {
		return r.RegistryHost
	}
	return c.Mirror().RKE2SystemRegistry
}

func (c *Config) Scoped(p string) string {
	if c.EnvironmentName == "" {
		return p
	}
	return p + "/" + c.EnvironmentName
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
	mr := c.Mirror()
	vars := c.VSphereVars(c.VSphereNamed(r.VSphereTarget))
	for k, v := range map[string]string{
		"rke2_cluster_name":       d.Name,
		"rke2_k8s_version":        r.RKE2Version,
		"rke2_cni":                r.CNIPlugin,
		"rke2_master_ips":         jsonOf(r.ControlPlaneNodeIPs),
		"rke2_master_cpu":         strconv.Itoa(r.ControlPlaneNodeSize.CPUCores),
		"rke2_master_memory_mb":   strconv.Itoa(r.ControlPlaneNodeSize.MemoryMB),
		"rke2_master_disk_gb":     strconv.Itoa(r.ControlPlaneNodeSize.DiskGB),
		"rke2_worker_ips":         jsonOf(append([]string{}, r.WorkerNodeIPs...)),
		"rke2_worker_cpu":         strconv.Itoa(r.WorkerNodeSize.CPUCores),
		"rke2_worker_memory_mb":   strconv.Itoa(r.WorkerNodeSize.MemoryMB),
		"rke2_worker_disk_gb":     strconv.Itoa(r.WorkerNodeSize.DiskGB),
		"rke2_registry_host":      c.RegistryHostFor(r),
		"rke2_registry_ip":        r.RegistryIP,
		"rke2_registry_auth":      strconv.FormatBool(!mr.Anonymous()),
		"mirror_registry_rewrite": mr.RegistryRewrite,
		"rancher_api_url":         "https://" + c.RancherHostname(),
		"rancher_ingress_ip":      c.Targets.Management.IngressLoadBalancerIP,
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
	mr := c.Mirror()
	e := map[string]string{
		"ARTIFACTORY_URL":            mr.FilesBase,
		"ARTIFACTORY_USER":           mr.Username,
		"ARTIFACTORY_PASSWORD":       mr.Password,
		"ARTIFACTORY_GENERIC_REPO":   mr.FilesRepo,
		"ARTIFACTORY_RPM_REPO":       mr.RPMRepo,
		"ARTIFACTORY_DOCKER_REPO":    mr.ImagesRepo,
		"ARTIFACTORY_HELM_REPO":      mr.ChartsRepo,
		"REGISTRY_HOST":              mr.OCIRegistry,
		"INGRESS_BASE_DOMAIN":        c.IngressBaseDomain,
		"INGRESS_IP":                 m.IngressLoadBalancerIP,
		"RANCHER_HOSTNAME":           c.RancherHostname(),
		"RANCHER_BOOTSTRAP_PASSWORD": c.Secrets.RancherBootstrapPassword,

		"TF_VAR_mirror_files_url":        mr.FilesRepoURL(),
		"TF_VAR_mirror_registry":         mr.OCIRegistry,
		"TF_VAR_mirror_registry_rewrite": mr.RegistryRewrite,
		"TF_VAR_mirror_username":         mr.Username,
		"TF_VAR_mirror_password":         mr.Password,
		"TF_VAR_environment":             c.EnvironmentName,
		"TF_VAR_vms":                     vmsVar(m.ServerNodes),
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
