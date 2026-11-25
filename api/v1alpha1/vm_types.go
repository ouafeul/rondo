package v1alpha1

import (
	"fmt"
	"time"
)

// +kubebuilder:validation:XValidation:rule="self.groups.exists(g, g.replicas > 0)",message="at least one group must have replicas > 0"
type VMSpec struct {
	// one per role
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Groups []VMGroup `json:"groups"`

	// vSphere template to clone; empty => ${target.values.vmTemplate}
	// +optional
	Template string `json:"template,omitempty"`

	// default sizing, overridable per group
	// +optional
	Sizing *VMSizing `json:"sizing,omitempty"`

	// Artifactory YUM repos
	// +optional
	RPMRepos []RPMRepo `json:"rpmRepos,omitempty"`

	// +optional
	Artifacts []VMArtifact `json:"artifacts,omitempty"`

	// +optional
	Installer *VMInstaller `json:"installer,omitempty"`

	// shared by all groups; secrets go in installer.secretsFrom
	// +kubebuilder:validation:MaxProperties=256
	// +optional
	Config map[string]VMConfigValue `json:"config,omitempty"`

	// a playbook shipped by the component
	// +optional
	Ansible *VMAnsible `json:"ansible,omitempty"`
}

// VMGroup is a set of machines sharing one role.
type VMGroup struct {
	// role, also the Ansible inventory group
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=32
	Name string `json:"name"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32
	// +kubebuilder:default=1
	Replicas int `json:"replicas"`

	// +optional
	Sizing *VMSizing `json:"sizing,omitempty"`

	// overrides spec.vm.config
	// +kubebuilder:validation:MaxProperties=256
	// +optional
	Config map[string]VMConfigValue `json:"config,omitempty"`

	// Target value holding this group's IPs (JSON list); empty => vmIPs.<component>.<group>
	// +optional
	IPsFromTargetValue string `json:"ipsFromTargetValue,omitempty"`
}

func (g VMGroup) IPsKey(component string) string {
	if g.IPsFromTargetValue != "" {
		return g.IPsFromTargetValue
	}
	return ValueVMIPsPrefix + component + "." + g.Name
}

// VMSizing is the sizing of one machine.
type VMSizing struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=64
	CPU int `json:"cpu"`
	// +kubebuilder:validation:Minimum=512
	MemoryMB int `json:"memoryMb"`
	// +kubebuilder:validation:Minimum=10
	DiskGB int `json:"diskGb"`
}

// RPMRepo is an Artifactory YUM repo configured on the machines.
type RPMRepo struct {
	// repo id in /etc/yum.repos.d/<name>.repo
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([-_.a-zA-Z0-9]*[a-zA-Z0-9])?$`
	Name string `json:"name"`

	// empty => Name
	// +optional
	Description string `json:"description,omitempty"`

	// Artifactory repo name
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// subpath in that repo, e.g. rhel9/9.6/baseos
	// +optional
	Path string `json:"path,omitempty"`

	// +kubebuilder:default=true
	// +optional
	GPGCheck *bool `json:"gpgCheck,omitempty"`

	// required when GPGCheck
	// +optional
	GPGKey string `json:"gpgKey,omitempty"`

	// needed for flat-mirrored modular packages: mirroring drops modules.yaml
	// +kubebuilder:default=false
	// +optional
	ModuleHotfixes bool `json:"moduleHotfixes,omitempty"`

	// lower = preferred
	// +optional
	Priority int `json:"priority,omitempty"`
}

func (r RPMRepo) GPGCheckEnabled() bool { return boolOr(r.GPGCheck, true) }

// ArtifactProvenance says who brings an artifact across the air gap.
// +kubebuilder:validation:Enum=upstream;component
type ArtifactProvenance string

const (
	ProvenanceUpstream  ArtifactProvenance = "upstream"
	ProvenanceComponent ArtifactProvenance = "component"
)

// VMConfigFormat is the shape of the rendered config file.
// +kubebuilder:validation:Enum=shell;env;yaml;json
type VMConfigFormat string

const (
	ConfigFormatShell VMConfigFormat = "shell"
	ConfigFormatEnv   VMConfigFormat = "env"
	ConfigFormatYAML  VMConfigFormat = "yaml"
	ConfigFormatJSON  VMConfigFormat = "json"
)

// VMArtifact is an archive dropped on the machines before installation.
type VMArtifact struct {
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// +kubebuilder:default=upstream
	// +optional
	Provenance ArtifactProvenance `json:"provenance,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// checked on the machine
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +optional
	Digest string `json:"digest,omitempty"`

	// relative to the component work directory
	// +optional
	Dest string `json:"dest,omitempty"`

	// +kubebuilder:default=false
	// +optional
	Extract bool `json:"extract,omitempty"`
}

const DefaultInstallPhaseName = "install"

// VMInstaller runs the installer shipped by the component, as one script or phases.
// +kubebuilder:validation:XValidation:rule="has(self.phases) != has(self.script)",message="set either script (single entry point) or phases (sequence), never both"
// +kubebuilder:validation:XValidation:rule="!has(self.phases) || (!has(self.args) && !has(self.steps) && !has(self.skipSteps))",message="args, steps and skipSteps belong to the phase when phases is set; leaving them here has no effect"
type VMInstaller struct {
	// ordered; a failing phase aborts the rest
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Phases []VMInstallPhase `json:"phases,omitempty"`

	// short form of a single phase
	// +kubebuilder:validation:MinLength=1
	// +optional
	Script string `json:"script,omitempty"`

	// verbatim; include the non-interactive flag
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	// +optional
	Args []string `json:"args,omitempty"`

	// rendered once next to the scripts, shared by all phases
	// +optional
	ConfigFile string `json:"configFile,omitempty"`

	// shape of that file
	// +kubebuilder:default=shell
	// +optional
	ConfigFormat VMConfigFormat `json:"configFormat,omitempty"`

	// 0600, shredded after the last phase
	// +optional
	SecretsFile string `json:"secretsFile,omitempty"`

	// --steps, short form
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	Steps []string `json:"steps,omitempty"`

	// --skip, short form
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	SkipSteps []string `json:"skipSteps,omitempty"`

	// defaults to --steps
	// +optional
	StepsFlag string `json:"stepsFlag,omitempty"`
	// +optional
	SkipFlag string `json:"skipFlag,omitempty"`

	// empty => the files are only dropped next to the scripts
	// +optional
	ConfigFlag string `json:"configFlag,omitempty"`
	// +optional
	SecretsFlag string `json:"secretsFlag,omitempty"`

	// shared by all phases
	// +optional
	SecretsFrom []ValuesSource `json:"secretsFrom,omitempty"`

	// install content version
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// sudo
	// +kubebuilder:default=true
	// +optional
	Become *bool `json:"become,omitempty"`

	// bound of one phase, Go notation (45m); empty => derived from the Job budget
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	// +optional
	Timeout *string `json:"timeout,omitempty"`
}

type VMInstallPhase struct {
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=32
	Name string `json:"name"`

	// relative to the work directory
	// +kubebuilder:validation:MinLength=1
	Script string `json:"script"`

	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	// +optional
	Args []string `json:"args,omitempty"`

	// passed with the installer's stepsFlag
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	Steps []string `json:"steps,omitempty"`

	// passed with the installer's skipFlag
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	SkipSteps []string `json:"skipSteps,omitempty"`

	// overrides the installer bound for this phase
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	// +optional
	Timeout *string `json:"timeout,omitempty"`
}

func (i VMInstaller) BecomeEnabled() bool { return boolOr(i.Become, true) }

func (i VMInstaller) StepsFlagOr() string {
	if i.StepsFlag != "" {
		return i.StepsFlag
	}
	return "--steps"
}

func (i VMInstaller) SkipFlagOr() string {
	if i.SkipFlag != "" {
		return i.SkipFlag
	}
	return "--skip"
}

func (i VMInstaller) ConfigFormatOr() VMConfigFormat {
	if i.ConfigFormat == "" {
		return ConfigFormatShell
	}
	return i.ConfigFormat
}

func (i VMInstaller) Sequence() []VMInstallPhase {
	if len(i.Phases) > 0 {
		return i.Phases
	}
	if i.Script == "" {
		return nil
	}
	return []VMInstallPhase{{
		Name:      DefaultInstallPhaseName,
		Script:    i.Script,
		Args:      i.Args,
		Steps:     i.Steps,
		SkipSteps: i.SkipSteps,
		Timeout:   i.Timeout,
	}}
}

func (i VMInstaller) TimeoutDuration() (time.Duration, error) {
	return parseInstallTimeout("installer.timeout", i.Timeout)
}

func (p VMInstallPhase) TimeoutDuration() (time.Duration, error) {
	return parseInstallTimeout("phase "+p.Name+" : timeout", p.Timeout)
}

func parseInstallTimeout(what string, raw *string) (time.Duration, error) {
	if raw == nil || *raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(*raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a Go duration (want `45m`, `1h30m`)", what, *raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be strictly positive: a zero bound "+
			"would kill the installer before it starts", what, *raw)
	}
	return d, nil
}

// VMAnsible describes Ansible content shipped by the component.
type VMAnsible struct {
	// Repo and Path locate the content tarball on Artifactory
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// +kubebuilder:default="site.yml"
	// +optional
	Playbook string `json:"playbook,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// +optional
	ExtraVars map[string]string `json:"extraVars,omitempty"`
}

// VMConfigValue is a string, bool, number or string list.
// +kubebuilder:validation:XValidation:rule="(has(self.value) ? 1 : 0) + (has(self.list) ? 1 : 0) + (has(self.bool) ? 1 : 0) + (has(self.number) ? 1 : 0) == 1",message="exactly one of value, list, bool or number"
type VMConfigValue struct {
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	Value *string `json:"value,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MaxLength=1024
	// +optional
	List []string `json:"list,omitempty"`
	// Rendered unquoted.
	// +optional
	Bool *bool `json:"bool,omitempty"`
	// Rendered unquoted.
	// +optional
	Number *int64 `json:"number,omitempty"`
}

// VSpherePlacement is where a vSphere Target creates its machines.
type VSpherePlacement struct {
	// keys vsphere_user and vsphere_password
	CredentialsSecretRef *LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// server, datacenter, datastore, network, folder, gateway
	SettingsConfigMapRef *LocalObjectReference `json:"settingsConfigMapRef,omitempty"`
}

const (
	ValueVMTemplate  = "vmTemplate"
	ValueSSHUser     = "sshUser"
	ValueVMIPsPrefix = "vmIPs."
)
