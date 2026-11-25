package kobj

const (
	PlatformNamespace = "rondo-system"

	APIGroup   = "rondo.ouafeul.github.io"
	APIVersion = APIGroup + "/v1alpha1"

	LabelName = "app.kubernetes.io/name"
)

const (
	ControllerName     = "rondo-controller"
	ProbeName          = "rondo-probe"
	DashboardName      = "rondo-dashboard"
	ExecServiceAccount = "rondo-exec"

	// DashboardPort is the dashboard Service's, which `rondo tui` port-forwards.
	DashboardPort = 80

	CredentialsSecret    = "rondo-credentials"
	ArtifactoryCACM      = "artifactory-ca"
	VMSSHSecret          = "rondo-vm-ssh"
	TofurcSecret         = "rondo-tofurc"
	PlatformValuesSecret = "rondo-platform-values"
)

const (
	CredentialsUserKey     = "ARTIFACTORY_USER"
	CredentialsPasswordKey = "ARTIFACTORY_PASSWORD"
	// The keys of the s3 state backend in the credentials Secret, named as OpenTofu reads them.
	StateAccessKeyEnv  = "AWS_ACCESS_KEY_ID"
	StateSecretKeyEnv  = "AWS_SECRET_ACCESS_KEY"
	ArtifactoryCAKey   = "ca.pem"
	TofurcKey          = ".tofurc"
	TofurcTokenKey     = "token"
	KubeconfigKey      = "kubeconfig"
	VMSSHPrivateKey    = "ssh-privatekey"
	VMSSHPublicKey     = "ssh-publickey"
	VSphereUserKey     = "vsphere_user"
	VSpherePasswordKey = "vsphere_password"
)

// Environment of rondo-controller: its Deployment sets it, main reads it.
const (
	EnvPodName           = "POD_NAME"
	EnvNamespace         = "RONDO_NAMESPACE"
	EnvToolboxImage      = "RONDO_TOOLBOX_IMAGE"
	EnvExecSA            = "RONDO_EXEC_SERVICE_ACCOUNT"
	EnvCACM              = "RONDO_ARTIFACTORY_CA_CONFIGMAP"
	EnvCredsSecret       = "RONDO_ARTIFACTORY_CREDS_SECRET"
	EnvTofuStateURL      = "RONDO_TOFU_STATE_URL"
	EnvTofuStateS3       = "RONDO_TOFU_STATE_S3"
	EnvTofuRCSecret      = "RONDO_TOFU_RC_SECRET"
	EnvVMModuleURL       = "RONDO_VM_MODULE_URL"
	EnvAnsibleContentURL = "RONDO_ANSIBLE_CONTENT_URL"
	EnvVMSSHSecret       = "RONDO_VM_SSH_SECRET"
	EnvArtifactoryURL    = "ARTIFACTORY_URL"
)

const (
	DefaultStorageClass = "local-path"
	VMSSHUser           = "rondo"
)

const (
	IngressControllerNamespace = "kube-system"

	CoreDNSNamespace   = "kube-system"
	CoreDNSConfigMap   = "coredns-custom"
	CoreDNSOverrideKey = "rondo-ingress-wildcard.override"
	CoreDNSDeployment  = "coredns"

	RegcredName      = "regcred"
	RegcredNamespace = "fleet-default"
)

const (
	netpolPrefix = "rondo-"
	nonRootUID   = 65532
)

func VSphereObjectName(target string) string { return "rondo-vsphere-" + target }

func KubeconfigSecretName(target string) string { return "rondo-kubeconfig-" + target }

// RecordName is the ConfigMap that remembers what an install applied, for the next prune.
func RecordName(tree string) string { return "rondo-component-" + tree }
