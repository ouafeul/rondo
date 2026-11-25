package v1alpha1

import (
	"reflect"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func strPtr(s string) *string { return &s }
func intPtr(i int64) *int64   { return &i }

func fullComponent() *Component {
	timeout := "30m"
	return &Component{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Component"},
		ObjectMeta: metav1.ObjectMeta{Name: "mft", Namespace: "rondo-system"},
		Spec: ComponentSpec{
			Chart:  &ChartSource{OCI: "oci://x/y", Version: "1.0.0", Published: Bool(true)},
			Values: &apiextensionsv1.JSON{Raw: []byte(`{"a":1}`)},
			VM: &VMSpec{
				Template: "rhel9",
				Sizing:   &VMSizing{CPU: 4, MemoryMB: 8192, DiskGB: 100},
				Groups: []VMGroup{{
					Name: "prod", Replicas: 2,
					Sizing: &VMSizing{CPU: 2, MemoryMB: 4096, DiskGB: 50},
					Config: map[string]VMConfigValue{
						"ROLE":  {Value: strPtr("prod")},
						"HOSTS": {List: []string{"a", "b"}},
						"FLAG":  {Bool: Bool(true)},
						"NUM":   {Number: intPtr(12)},
					},
					IPsFromTargetValue: "ips",
				}},
				RPMRepos: []RPMRepo{{
					Name: "r", Description: "d", Repo: "rondo-rpms-local", Path: "el9",
					GPGCheck: Bool(false), GPGKey: "k", ModuleHotfixes: true, Priority: 1,
				}},
				Artifacts: []VMArtifact{{
					Repo: "g", Path: "p", Digest: "sha256:x", Dest: "d", Extract: true,
				}},
				Config: map[string]VMConfigValue{"K": {Value: strPtr("v")}},
				Installer: &VMInstaller{
					Phases: []VMInstallPhase{{
						Name: "base", Script: "b.sh", Args: []string{"-x"},
						Steps: []string{"a"}, SkipSteps: []string{"b"}, Timeout: &timeout,
					}},
					Script: "i.sh", Args: []string{"--yes"},
					ConfigFile: "c", SecretsFile: "s",
					Steps: []string{"a"}, SkipSteps: []string{"repo"},
					StepsFlag: "-s", SkipFlag: "-S",
					ConfigFlag: "-c", SecretsFlag: "-e",
					SecretsFrom: []ValuesSource{{SecretRef: &LocalObjectReference{Name: "sec"}, Keys: []string{"P"}}},
					Version:     "1.0", Become: Bool(true), Timeout: &timeout,
				},
				Ansible: &VMAnsible{
					Repo: "g", Path: "p", Playbook: "site.yml", Version: "1.0",
					ExtraVars: map[string]string{"a": "b"},
				},
			},
			ValuesFrom: []ValuesSource{{SecretRef: &LocalObjectReference{Name: "s"}, Keys: []string{"k"}}},
			DependsOn:  []LocalObjectReference{{Name: "dep"}},
			Health:     []HealthRule{{Group: "apps", Kind: "Deployment", Condition: "Available"}},
		},
	}
}

func fullTarget() *Target {
	return &Target{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Target"},
		ObjectMeta: metav1.ObjectMeta{Name: "dmz", Namespace: "rondo-system"},
		Spec: TargetSpec{
			Roles:  []string{"dmz"},
			Values: map[string]string{"a": "b"},
			Kubernetes: &KubernetesCluster{
				Server:              "https://x:6443",
				KubeconfigSecretRef: &SecretKeySelector{Name: "kc", Key: "k"},
			},
			VSphere: &VSpherePlacement{
				CredentialsSecretRef: &LocalObjectReference{Name: "c"},
				SettingsConfigMapRef: &LocalObjectReference{Name: "s"},
			},
		},
		Status: TargetStatus{
			Conditions: []metav1.Condition{{Type: "Ready", Status: "True"}},
		},
	}
}

func fullEnrollment() *Enrollment {
	d := metav1.Duration{Duration: time.Minute}
	now := metav1.Now()
	return &Enrollment{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Enrollment"},
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "rondo-system"},
		Spec: EnrollmentSpec{
			Component:  "c",
			Target:     "dmz",
			Values:     &apiextensionsv1.JSON{Raw: []byte(`{}`)},
			ValuesFrom: []ValuesSource{{SecretRef: &LocalObjectReference{Name: "s"}, Keys: []string{"k"}}},
			Policy:     DeliveryPolicy{CreateNamespace: Bool(true), Atomic: Bool(true), Wait: Bool(true), Timeout: &d},
		},
		Status: EnrollmentStatus{
			Conditions:        []metav1.Condition{{Type: "Ready", Status: "True"}},
			ResolvedTarget:    "dmz",
			MachinesSecretRef: &LocalObjectReference{Name: "m"},
			ActiveJobRef:      &LocalObjectReference{Name: "j"},
			Inventory:         []InventoryEntry{{Kind: "ConfigMap", Name: "c"}},
			LastAttempt:       &AttemptInfo{JobName: "j", StartedAt: now, FinishedAt: &now},
			History:           []AttemptInfo{{JobName: "j", StartedAt: now}},
		},
	}
}

func TestDeepCopyIsIndependent(t *testing.T) {
	cases := []struct {
		name   string
		obj    runtime.Object
		mutate func(runtime.Object)
	}{
		{"Component", fullComponent(), func(o runtime.Object) {
			c := o.(*Component)
			c.Spec.VM.Groups[0].Config["HOSTS"].List[0] = "mutated"
			c.Spec.VM.Installer.Phases[0].Args[0] = "mutated"
			c.Spec.VM.Installer.Args[0] = "mutated"
			c.Spec.VM.RPMRepos[0].Name = "mutated"
			c.Spec.VM.Config["K"] = VMConfigValue{Value: strPtr("mutated")}
			c.Spec.VM.Ansible.ExtraVars["a"] = "mutated"
			c.Spec.Values.Raw[0] = 'X'
			c.Spec.DependsOn[0].Name = "mutated"
			c.Spec.Health[0].Kind = "mutated"
		}},
		{"Target", fullTarget(), func(o runtime.Object) {
			tg := o.(*Target)
			tg.Spec.Roles[0] = "mutated"
			tg.Spec.Values["a"] = "mutated"
			tg.Spec.Kubernetes.KubeconfigSecretRef.Name = "mutated"
			tg.Spec.VSphere.CredentialsSecretRef.Name = "mutated"
		}},
		{"Enrollment", fullEnrollment(), func(o runtime.Object) {
			e := o.(*Enrollment)
			e.Spec.Values.Raw[0] = 'X'
			e.Spec.ValuesFrom[0].Keys[0] = "mutated"
			*e.Spec.Policy.Wait = false
			e.Status.Inventory[0].Name = "mutated"
			e.Status.History[0].JobName = "mutated"
			e.Status.LastAttempt.JobName = "mutated"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copied := tc.obj.DeepCopyObject()
			before := copied.DeepCopyObject()
			tc.mutate(tc.obj)
			if !reflect.DeepEqual(copied, before) {
				t.Errorf("%s: the copy followed the original's mutation, "+
					"SHALLOW copy somewhere (regenerate with scripts/ci/gen-crds.sh)",
					tc.name)
			}
		})
	}
}

func TestDeepCopyPreservesContent(t *testing.T) {
	c, tg, e := fullComponent(), fullTarget(), fullEnrollment()
	for _, obj := range []runtime.Object{
		c, tg, e,
		&ComponentList{Items: []Component{*c}},
		&TargetList{Items: []Target{*tg}},
		&EnrollmentList{Items: []Enrollment{*e}},
	} {
		if got := obj.DeepCopyObject(); !reflect.DeepEqual(got, obj) {
			t.Errorf("%T: the copy differs from the original", obj)
		}
	}
	for _, obj := range []runtime.Object{
		&Component{}, &Target{}, &Enrollment{},
		&ComponentList{}, &TargetList{}, &EnrollmentList{},
	} {
		if got := obj.DeepCopyObject(); got == nil {
			t.Errorf("%T: DeepCopyObject returned nil", obj)
		}
	}
	vm := c.Spec.VM
	hosts := vm.Groups[0].Config["HOSTS"]
	for name, pair := range map[string][2]any{
		"ChartSource":       {*c.Spec.Chart.DeepCopy(), *c.Spec.Chart},
		"HealthRule":        {*c.Spec.Health[0].DeepCopy(), c.Spec.Health[0]},
		"VMSpec":            {*vm.DeepCopy(), *vm},
		"VMGroup":           {*vm.Groups[0].DeepCopy(), vm.Groups[0]},
		"VMSizing":          {*vm.Sizing.DeepCopy(), *vm.Sizing},
		"VMConfigValue":     {*hosts.DeepCopy(), hosts},
		"RPMRepo":           {*vm.RPMRepos[0].DeepCopy(), vm.RPMRepos[0]},
		"VMArtifact":        {*vm.Artifacts[0].DeepCopy(), vm.Artifacts[0]},
		"VMInstaller":       {*vm.Installer.DeepCopy(), *vm.Installer},
		"VMInstallPhase":    {*vm.Installer.Phases[0].DeepCopy(), vm.Installer.Phases[0]},
		"VMAnsible":         {*vm.Ansible.DeepCopy(), *vm.Ansible},
		"ValuesSource":      {*c.Spec.ValuesFrom[0].DeepCopy(), c.Spec.ValuesFrom[0]},
		"LocalObjectRef":    {*c.Spec.DependsOn[0].DeepCopy(), c.Spec.DependsOn[0]},
		"KubernetesCluster": {*tg.Spec.Kubernetes.DeepCopy(), *tg.Spec.Kubernetes},
		"SecretKeySelector": {*tg.Spec.Kubernetes.KubeconfigSecretRef.DeepCopy(), *tg.Spec.Kubernetes.KubeconfigSecretRef},
		"VSpherePlacement":  {*tg.Spec.VSphere.DeepCopy(), *tg.Spec.VSphere},
		"TargetStatus":      {*tg.Status.DeepCopy(), tg.Status},
		"DeliveryPolicy":    {*e.Spec.Policy.DeepCopy(), e.Spec.Policy},
		"EnrollmentStatus":  {*e.Status.DeepCopy(), e.Status},
		"InventoryEntry":    {*e.Status.Inventory[0].DeepCopy(), e.Status.Inventory[0]},
		"AttemptInfo":       {*e.Status.LastAttempt.DeepCopy(), *e.Status.LastAttempt},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s: the copy differs from the original", name)
		}
	}
}
