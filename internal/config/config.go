package config

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/go-playground/validator/v10"
	"sigs.k8s.io/yaml"
)

type Config struct {
	EnvironmentName   string    `json:"environmentName,omitempty" validate:"omitempty,max=32,ne=default,excludes=.,lowercase,hostname_rfc1123"`
	IngressBaseDomain string    `json:"ingressBaseDomain" validate:"required,fqdn"`
	Artifacts         Artifacts `json:"artifacts" validate:"required"`
	Targets           Targets   `json:"targets" validate:"required"`
	Secrets           Secrets   `json:"secrets" validate:"required"`
}

// The artifact mirror presets: generic declares each endpoint, artifactory derives them all from
// the URL of a JFrog Artifactory.
const (
	PresetGeneric     = "generic"
	PresetArtifactory = "artifactory"
)

// Artifacts is the mirror every artifact comes from: images and Helm charts (OCI), files, RPM
// packages, OpenTofu providers and states. Every repository is a path segment under its root, as
// in <root>/<repository>/<path>. Mirror resolves the endpoints.
type Artifacts struct {
	// Preset is generic when empty.
	Preset string `json:"preset,omitempty" validate:"omitempty,oneof=generic artifactory"`
	// Username and secrets.mirrorPassword authenticate against every endpoint; both empty, the
	// mirror is read anonymously.
	Username string `json:"username,omitempty"`
	// CACertificateFile is the PEM bundle trusted for every endpoint; empty, the system trust
	// store is.
	CACertificateFile string `json:"caCertificateFile,omitempty"`
	ReleaseChannel    string `json:"releaseChannel,omitempty"`

	OCI   OCIEndpoint   `json:"oci,omitempty"`
	Files FilesEndpoint `json:"files,omitempty"`
	RPM   RPMEndpoint   `json:"rpm,omitempty"`
	Tofu  TofuEndpoints `json:"tofu,omitempty"`

	Artifactory ArtifactoryMirror `json:"artifactory,omitempty"`
}

// OCIEndpoint is the registry of the images (<registry>/<repository>/<image>) and of the Helm
// charts (oci://<registry>/<chartsRepository>/<chart>, chartsRepository <repository>/charts by
// default).
type OCIEndpoint struct {
	// Registry is host[:port], without a scheme.
	Registry         string `json:"registry,omitempty"`
	Repository       string `json:"repository,omitempty"`
	ChartsRepository string `json:"chartsRepository,omitempty"`
}

// FilesEndpoint is the HTTP file server: RONDO's own files live under <url>/<repository>/.
type FilesEndpoint struct {
	URL        string `json:"url,omitempty" validate:"omitempty,url"`
	Repository string `json:"repository,omitempty"`
}

// RPMEndpoint is the root of the dnf repositories, <url>/<repository>/<path>.
type RPMEndpoint struct {
	URL        string `json:"url,omitempty" validate:"omitempty,url"`
	Repository string `json:"repository,omitempty"`
}

// TofuEndpoints are where OpenTofu installs its providers from, ProviderMirror (a network
// mirror) or ProviderOCI (an OCI mirror), and where it keeps its states.
type TofuEndpoints struct {
	ProviderMirror string `json:"providerMirror,omitempty" validate:"omitempty,url"`
	// ProviderOCI is <registry>/<repository>: provider <namespace>/<type> is the OCI repository
	// <registry>/<repository>/<namespace>/<type>, one tag per version.
	ProviderOCI string    `json:"providerOCI,omitempty"`
	State       TofuState `json:"state,omitempty"`
}

// TofuState is the state backend: http or s3, exactly one.
type TofuState struct {
	HTTP *HTTPState `json:"http,omitempty"`
	S3   *S3State   `json:"s3,omitempty"`
}

// HTTPState is the base URL of the http backend: a state is read, written and deleted (GET, PUT,
// DELETE) at <url>/<environment>/<stack>/terraform.tfstate, with the mirror's basic auth.
type HTTPState struct {
	URL string `json:"url" validate:"required,url"`
}

// S3State is a bucket of an S3 API (MinIO, Ceph, AWS...): a state is the object
// <keyPrefix>/<environment>/<stack>/terraform.tfstate, locked by a lock file next to it.
type S3State struct {
	Bucket string `json:"bucket" validate:"required"`
	// Endpoint is the URL of the S3 API; empty, AWS's.
	Endpoint string `json:"endpoint,omitempty" validate:"omitempty,url"`
	// Region is us-east-1 when empty.
	Region    string `json:"region,omitempty"`
	KeyPrefix string `json:"keyPrefix,omitempty"`
	// UsePathStyle addresses the bucket as <endpoint>/<bucket> instead of <bucket>.<endpoint host>.
	UsePathStyle bool `json:"usePathStyle,omitempty"`
}

// ArtifactoryMirror is the preset artifactory: every endpoint derives from URL.
type ArtifactoryMirror struct {
	URL          string                  `json:"url,omitempty" validate:"omitempty,url"`
	Repositories ArtifactoryRepositories `json:"repositories,omitempty"`
	// empty never expires, which Artifactory grants to admin accounts only
	MirrorTokenLifetimeSeconds string `json:"mirrorTokenLifetimeSeconds,omitempty" validate:"omitempty,number"`
}

// ArtifactoryRepositories name the Artifactory repositories; Mirror fills in the defaults.
type ArtifactoryRepositories struct {
	Docker    string `json:"docker,omitempty"`
	Helm      string `json:"helm,omitempty"`
	Generic   string `json:"generic,omitempty"`
	RPM       string `json:"rpm,omitempty"`
	Tofu      string `json:"tofu,omitempty"`
	TofuState string `json:"tofuState,omitempty"`
}

// Targets is where components are installed: the vSphere environments that host the machines, the
// management cluster, and the downstream clusters it provisions on them.
type Targets struct {
	VSphere []VSphereTarget `json:"vsphere" validate:"required,min=1,dive"`
	// Management is the k3s cluster that runs the platform. Its Target is named, and holds the
	// role, ManagementRole.
	Management  K3sTarget          `json:"management" validate:"required"`
	Downstreams []DownstreamTarget `json:"downstreams,omitempty" validate:"dive"`
}

// Placement is what components see of a target: its name, its roles, and the values it adds.
type Placement struct {
	Name            string            `json:"name" validate:"required,hostname_rfc1123"`
	Roles           []string          `json:"roles,omitempty" validate:"dive,hostname_rfc1123"`
	ComponentValues map[string]string `json:"componentValues,omitempty"`
}

// VSphereTarget is a vSphere environment that can host VMs and/or clusters of any type.
type VSphereTarget struct {
	Placement
	VCenterAddress     string    `json:"vcenterAddress" validate:"required"`
	VCenterUsername    string    `json:"vcenterUsername" validate:"required"`
	AllowUnverifiedTLS bool      `json:"allowUnverifiedTls"`
	Datacenter         string    `json:"datacenter" validate:"required"`
	ComputeCluster     string    `json:"computeCluster" validate:"required"`
	Datastore          string    `json:"datastore" validate:"required"`
	VMFolder           string    `json:"vmFolder,omitempty"`
	ResourcePool       string    `json:"resourcePool,omitempty"`
	VMTemplate         string    `json:"vmTemplate" validate:"required"`
	VMNetwork          VMNetwork `json:"vmNetwork" validate:"required"`
}

type VMNetwork struct {
	PortGroup       string   `json:"portGroup" validate:"required"`
	GatewayIP       string   `json:"gatewayIp" validate:"required,ip"`
	NetmaskBits     int      `json:"netmaskBits" validate:"required,min=1,max=32"`
	DNSServerIPs    []string `json:"dnsServerIps" validate:"required,min=1,dive,ip"`
	DNSSearchDomain string   `json:"dnsSearchDomain,omitempty"`
}

// DownstreamTarget is an rke2 cluster the management cluster provisions on a vSphere target.
type DownstreamTarget struct {
	Placement
	RKE2 RKE2Target `json:"rke2" validate:"required"`
}

type K3sTarget struct {
	VSphereTarget         string   `json:"vsphereTarget" validate:"required"`
	K3sVersion            string   `json:"k3sVersion" validate:"required"`
	IngressLoadBalancerIP string   `json:"ingressLoadBalancerIp" validate:"required,ip"`
	ServerNodes           []Node   `json:"serverNodes" validate:"required,len=3,dive"`
	ServerNodeSize        NodeSize `json:"serverNodeSize" validate:"required"`
	NodeSSHUsername       string   `json:"nodeSshUsername" validate:"required"`
	NodeSSHPassword       string   `json:"nodeSshPassword" validate:"required"`
}

// RKE2Target is the cluster itself. RegistryHost overrides the registry authority, which defaults to
// the mirror's (Mirror.RKE2SystemRegistry); RegistryIP is its address for /etc/hosts on the nodes, for
// sites where DNS does not resolve it.
type RKE2Target struct {
	VSphereTarget         string   `json:"vsphereTarget" validate:"required"`
	RKE2Version           string   `json:"rke2Version" validate:"required"`
	CNIPlugin             string   `json:"cniPlugin" validate:"required,oneof=canal calico cilium"`
	IngressLoadBalancerIP string   `json:"ingressLoadBalancerIp" validate:"required,ip"`
	ControlPlaneNodeIPs   []string `json:"controlPlaneNodeIps" validate:"required,min=1,dive,ip"`
	ControlPlaneNodeSize  NodeSize `json:"controlPlaneNodeSize" validate:"required"`
	WorkerNodeIPs         []string `json:"workerNodeIps,omitempty" validate:"dive,ip"`
	WorkerNodeSize        NodeSize `json:"workerNodeSize" validate:"required"`
	RegistryHost          string   `json:"registryHost,omitempty"`
	RegistryIP            string   `json:"registryIp,omitempty" validate:"omitempty,ip"`
}

type Node struct {
	Hostname string `json:"hostname" validate:"required,hostname_rfc1123"`
	IP       string `json:"ip" validate:"required,ip"`
}

type NodeSize struct {
	CPUCores int `json:"cpuCores" validate:"required,min=1"`
	MemoryMB int `json:"memoryMb" validate:"required,min=512"`
	DiskGB   int `json:"diskGb" validate:"required,min=10"`
}

type Secrets struct {
	MirrorPassword string `json:"mirrorPassword,omitempty"`
	MirrorToken    string `json:"mirrorToken,omitempty"`
	// MirrorStateAccessKey and MirrorStateSecretKey authenticate on the s3 state backend; both
	// empty, artifacts.username and mirrorPassword do.
	MirrorStateAccessKey     string                       `json:"mirrorStateAccessKey,omitempty"`
	MirrorStateSecretKey     string                       `json:"mirrorStateSecretKey,omitempty"`
	RancherBootstrapPassword string                       `json:"rancherBootstrapPassword" validate:"required"`
	K3sClusterToken          string                       `json:"k3sClusterToken" validate:"required"`
	VCenterPasswords         map[string]string            `json:"vcenterPasswords" validate:"required"`
	Components               map[string]map[string]string `json:"components,omitempty"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return parse(raw, path)
}

// Parse checks a manifest that is not on disk yet, as Load checks a file.
func Parse(raw []byte) (*Config, error) { return parse(raw, "rondo.yaml") }

func parse(raw []byte, name string) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", name, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) Validate() error {
	if err := validator.New(validator.WithRequiredStructEnabled()).Struct(c); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	if err := c.validateArtifacts(); err != nil {
		return err
	}
	if err := c.validateTargets(); err != nil {
		return err
	}
	if err := c.validateSecrets(); err != nil {
		return err
	}
	return checkComponentSecrets(c.Secrets.Components)
}

// ManagementRole is the name and the role of the management cluster's Target; no other target
// may take either, so a component tree can reach the management cluster by it.
const ManagementRole = "management"

// VSphereNamed is the vSphere target of that name, nil when the manifest declares none.
func (c *Config) VSphereNamed(name string) *VSphereTarget {
	for i := range c.Targets.VSphere {
		if c.Targets.VSphere[i].Name == name {
			return &c.Targets.VSphere[i]
		}
	}
	return nil
}

// validateTargets checks what struct tags cannot: names are unique, the management name and role
// are reserved, and every vsphereTarget names a declared vSphere target.
func (c *Config) validateTargets() error {
	if err := c.checkVSphereRef(ManagementRole, c.Targets.Management.VSphereTarget); err != nil {
		return err
	}
	placements := make([]Placement, 0, len(c.Targets.VSphere)+len(c.Targets.Downstreams))
	for _, v := range c.Targets.VSphere {
		placements = append(placements, v.Placement)
	}
	for _, d := range c.Targets.Downstreams {
		placements = append(placements, d.Placement)
	}
	names := map[string]bool{}
	for _, p := range placements {
		if names[p.Name] {
			return fmt.Errorf("targets: duplicate name %q", p.Name)
		}
		names[p.Name] = true
		if p.Name == ManagementRole || slices.Contains(p.Roles, ManagementRole) {
			return fmt.Errorf("target %s: the name and role %q are reserved for the management cluster",
				p.Name, ManagementRole)
		}
	}
	for _, d := range c.Targets.Downstreams {
		if err := c.checkVSphereRef(d.Name, d.RKE2.VSphereTarget); err != nil {
			return err
		}
		if len(d.RKE2.ControlPlaneNodeIPs)%2 == 0 {
			return fmt.Errorf("target %s: rke2.controlPlaneNodeIps must hold an odd "+
				"number of nodes (etcd quorum), found %d", d.Name, len(d.RKE2.ControlPlaneNodeIPs))
		}
	}
	return nil
}

func (c *Config) checkVSphereRef(owner, ref string) error {
	if c.VSphereNamed(ref) == nil {
		return fmt.Errorf("target %s: vsphereTarget %q names no vsphere target", owner, ref)
	}
	return nil
}

// validateSecrets expects a vCenter password for every vSphere target, and for no other.
func (c *Config) validateSecrets() error {
	for _, name := range sortedKeys(c.Secrets.VCenterPasswords) {
		if c.VSphereNamed(name) == nil {
			return fmt.Errorf("secrets.vcenterPasswords.%s: no vsphere target has this name", name)
		}
	}
	for _, v := range c.Targets.VSphere {
		if c.Secrets.VCenterPasswords[v.Name] == "" {
			return fmt.Errorf("secrets.vcenterPasswords.%s is required", v.Name)
		}
	}
	return nil
}

var (
	secretNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	secretKeyRe  = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

func checkComponentSecrets(secrets map[string]map[string]string) error {
	for _, name := range sortedKeys(secrets) {
		data := secrets[name]
		if !secretNameRe.MatchString(name) {
			return fmt.Errorf("secrets.components: %q is not a valid Secret name", name)
		}
		if len(data) == 0 {
			return fmt.Errorf("secrets.components.%s declares no key", name)
		}
		for _, key := range sortedKeys(data) {
			if !secretKeyRe.MatchString(key) {
				return fmt.Errorf("secrets.components.%s: %q is not a valid Secret key", name, key)
			}
			if strings.TrimSpace(data[key]) == "" {
				return fmt.Errorf("secrets.components.%s.%s is empty", name, key)
			}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
