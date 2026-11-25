package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/internal/kobj"
)

type Verb string

const (
	VerbHelmApply     Verb = "helm-apply"
	VerbHelmUninstall Verb = "helm-uninstall"
	VerbVMPlan        Verb = "vm-plan"
	VerbVMApply       Verb = "vm-apply"
	VerbVMDestroy     Verb = "vm-destroy"
	VerbVMInstall     Verb = "vm-install"
)

func (v Verb) Mutating() bool {
	switch v {
	case VerbVMPlan:
		return false
	default:
		return true
	}
}

func (v Verb) NeedsTofu() bool {
	switch v {
	case VerbVMPlan, VerbVMApply, VerbVMDestroy:
		return true
	default:
		return false
	}
}

func (v Verb) NeedsVMRuntime() bool {
	switch v {
	case VerbVMPlan, VerbVMApply, VerbVMDestroy, VerbVMInstall:
		return true
	default:
		return false
	}
}

const (
	LabelManagedBy   = "app.kubernetes.io/managed-by"
	LabelOwnerKind   = "rondo.ouafeul.github.io/owner-kind"
	LabelOwnerName   = "rondo.ouafeul.github.io/owner-name"
	LabelVerb        = "rondo.ouafeul.github.io/verb"
	LabelFingerprint = "rondo.ouafeul.github.io/fingerprint"

	ManagedByValue = "rondo-controller"
)

const maxNameLen = 52

type Config struct {
	Namespace              string
	Image                  string
	ServiceAccount         string
	ArtifactoryCAConfigMap string
	ArtifactoryCredsSecret string
	TTL                    time.Duration
	DefaultTimeout         time.Duration
	Resources              corev1.ResourceRequirements

	TofuStateBaseURL    string
	TofuStateS3         string
	TofuRCSecret        string
	AllowLocalTofuState bool

	VMModuleURL       string
	AnsibleContentURL string
	VMSSHSecret       string
	ArtifactoryURL    string
}

func (c Config) withDefaults() Config {
	if c.Namespace == "" {
		c.Namespace = kobj.PlatformNamespace
	}
	if c.ServiceAccount == "" {
		c.ServiceAccount = kobj.ExecServiceAccount
	}
	if c.TTL == 0 {
		c.TTL = 24 * time.Hour
	}
	if c.DefaultTimeout == 0 {
		c.DefaultTimeout = 10 * time.Minute
	}
	if len(c.Resources.Requests) == 0 && len(c.Resources.Limits) == 0 {
		c.Resources = defaultResources()
	}
	return c
}

func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}
}

func (c Config) TimeoutFor(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return c.withDefaults().DefaultTimeout
}

const LeaseGrace = 2 * time.Minute

func (c Config) LeaseTTLFor(d time.Duration) time.Duration {
	return c.TimeoutFor(d) + LeaseGrace
}

type Request struct {
	Verb         Verb
	Owner        client.Object
	OwnerGVK     schema.GroupVersionKind
	Fingerprint  string
	Timeout      time.Duration
	ExtraEnv     []corev1.EnvVar
	ExtraEnvFrom []corev1.EnvFromSource
}

func (c Config) Name(req Request) string {
	kind := strings.ToLower(req.OwnerGVK.Kind)
	fp := shortHash(req.Fingerprint)
	verb := strings.ReplaceAll(string(req.Verb), "-", "")
	base := fmt.Sprintf("%s-%s-%s-%s", kind, req.Owner.GetName(), verb, fp)
	if len(base) <= maxNameLen {
		return base
	}
	keep := maxNameLen - len(kind) - len(verb) - len(fp) - 3
	if keep < 1 {
		keep = 1
	}
	return fmt.Sprintf("%s-%s-%s-%s", kind, req.Owner.GetName()[:keep], verb, fp)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:10]
}

func (c Config) Build(req Request) (*batchv1.Job, error) {
	c = c.withDefaults()
	if req.Owner == nil {
		return nil, fmt.Errorf("jobs: Owner required")
	}
	if req.OwnerGVK.Kind == "" {
		return nil, fmt.Errorf("jobs: OwnerGVK.Kind required (ownerReference)")
	}
	if req.Verb == "" {
		return nil, fmt.Errorf("jobs: Verb required")
	}
	if req.Fingerprint == "" {
		return nil, fmt.Errorf("jobs: Fingerprint required (idempotency key of the name)")
	}
	if c.Image == "" {
		return nil, fmt.Errorf("jobs: toolbox Image required")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = c.DefaultTimeout
	}
	deadline := int64(timeout.Seconds())
	ttl := int32(c.TTL.Seconds())
	var backoff int32

	labels := map[string]string{
		LabelManagedBy:   ManagedByValue,
		LabelOwnerKind:   req.OwnerGVK.Kind,
		LabelOwnerName:   req.Owner.GetName(),
		LabelVerb:        string(req.Verb),
		LabelFingerprint: shortHash(req.Fingerprint),
	}

	name := c.Name(req)
	env := append([]corev1.EnvVar{
		{Name: "RONDO_TARGET_REF", Value: req.Owner.GetNamespace() + "/" + req.Owner.GetName()},
		{Name: "RONDO_TARGET_KIND", Value: req.OwnerGVK.Kind},
		{Name: "RONDO_NAMESPACE", Value: c.Namespace},
		{Name: "RONDO_JOB_NAME", Value: name},
		{Name: "HOME", Value: "/work"},
		{Name: "HELM_CACHE_HOME", Value: "/work/.helm/cache"},
		{Name: "HELM_CONFIG_HOME", Value: "/work/.helm/config"},
		{Name: "HELM_DATA_HOME", Value: "/work/.helm/data"},
		{Name: "TF_DATA_DIR", Value: "/work/.tofu"},
	}, req.ExtraEnv...)

	if req.Verb.NeedsTofu() {
		if c.TofuStateBaseURL != "" {
			env = append(env, corev1.EnvVar{Name: "RONDO_TOFU_STATE_URL", Value: c.TofuStateBaseURL})
		}
		if c.TofuStateS3 != "" {
			env = append(env, corev1.EnvVar{Name: "RONDO_TOFU_STATE_S3", Value: c.TofuStateS3})
		}
		if c.TofuRCSecret != "" {
			env = append(env, corev1.EnvVar{Name: "RONDO_TOFU_RC_SECRET", Value: c.TofuRCSecret})
		}
		if c.AllowLocalTofuState {
			env = append(env, corev1.EnvVar{Name: "RONDO_TOFU_ALLOW_LOCAL_STATE", Value: "1"})
		}
	}

	if req.Verb.NeedsVMRuntime() {
		if c.VMModuleURL != "" {
			env = append(env, corev1.EnvVar{Name: "RONDO_VM_MODULE_URL", Value: c.VMModuleURL})
		}
		if c.AnsibleContentURL != "" {
			env = append(env, corev1.EnvVar{Name: "RONDO_ANSIBLE_CONTENT_URL", Value: c.AnsibleContentURL})
		}
		if c.VMSSHSecret != "" {
			env = append(env, corev1.EnvVar{Name: "RONDO_VM_SSH_SECRET", Value: c.VMSSHSecret})
		}
		if c.ArtifactoryURL != "" {
			env = append(env, corev1.EnvVar{Name: "ARTIFACTORY_URL", Value: c.ArtifactoryURL})
		}
	}

	envFrom := req.ExtraEnvFrom
	if c.ArtifactoryCredsSecret != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: c.ArtifactoryCredsSecret},
			},
		})
	}

	tmpLimit := resource.MustParse("2Gi")
	volumes := []corev1.Volume{{
		Name:         "work",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}, {
		Name:         "tmp",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpLimit}},
	}}
	mounts := []corev1.VolumeMount{
		{Name: "work", MountPath: "/work"},
		{Name: "tmp", MountPath: "/tmp"},
	}

	if c.ArtifactoryCAConfigMap != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "artifactory-ca",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: c.ArtifactoryCAConfigMap},
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name: "artifactory-ca", MountPath: "/etc/rondo/ca", ReadOnly: true,
		})
		env = append(env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/etc/rondo/ca/" + kobj.ArtifactoryCAKey})
	}

	yes, no := true, false
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         req.OwnerGVK.GroupVersion().String(),
				Kind:               req.OwnerGVK.Kind,
				Name:               req.Owner.GetName(),
				UID:                req.Owner.GetUID(),
				Controller:         &yes,
				BlockOwnerDeletion: &yes,
			}},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: c.ServiceAccount,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &yes,
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Volumes: volumes,
					Containers: []corev1.Container{{
						Name:         "exec",
						Image:        c.Image,
						Args:         []string{"exec", string(req.Verb)},
						Env:          env,
						EnvFrom:      envFrom,
						VolumeMounts: mounts,
						Resources:    c.Resources,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &no,
							ReadOnlyRootFilesystem:   &yes,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
	return job, nil
}

func Finished(job *batchv1.Job) (done bool, succeeded bool) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, true
		case batchv1.JobFailed:
			return true, false
		}
	}
	return false, false
}

func FailureMessage(job *batchv1.Job) string {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			msg := strings.TrimSpace(c.Reason + " " + c.Message)
			if msg == "" {
				return "failed without a message (see the pod logs)"
			}
			return msg
		}
	}
	return ""
}
