package values

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"rondo/api/v1alpha1"
)

// VMInfraHash is what a VM Enrollment's provisioning depends on: a change replays vm-apply.
func VMInfraHash(vm *v1alpha1.VMSpec, component string, target *v1alpha1.Target) string {
	h := sha256.New()
	fmt.Fprintf(h, "template=%s\n", vm.Template)
	if vm.Sizing != nil {
		fmt.Fprintf(h, "sizing=%d/%d/%d\n", vm.Sizing.CPU, vm.Sizing.MemoryMB, vm.Sizing.DiskGB)
	}
	for _, g := range vm.Groups {
		fmt.Fprintf(h, "group=%s replicas=%d", g.Name, g.Replicas)
		if g.Sizing != nil {
			fmt.Fprintf(h, " sizing=%d/%d/%d", g.Sizing.CPU, g.Sizing.MemoryMB, g.Sizing.DiskGB)
		}
		fmt.Fprintf(h, " ips=%s\n", target.Spec.Values[g.IPsKey(component)])
	}
	fmt.Fprintf(h, "vmTemplate=%s\n", target.Spec.Values[v1alpha1.ValueVMTemplate])
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// VMInstallHash is what its installation depends on: a change replays vm-install.
func VMInstallHash(vm *v1alpha1.VMSpec, secrets map[string]string) string {
	h := sha256.New()
	writeConfig := func(prefix string, cfg map[string]v1alpha1.VMConfigValue) {
		keys := make([]string, 0, len(cfg))
		for k := range cfg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := cfg[k]
			fmt.Fprintf(h, "%s.%s=%v/%v/%v/%v\n", prefix, k,
				derefStr(v.Value), v.List, derefBool(v.Bool), derefInt(v.Number))
		}
	}
	writeConfig("cfg", vm.Config)
	for _, g := range vm.Groups {
		writeConfig("cfg."+g.Name, g.Config)
	}
	for _, r := range vm.RPMRepos {
		fmt.Fprintf(h, "repo=%s/%s/%s/%t/%s\n", r.Name, r.Repo, r.Path, r.GPGCheckEnabled(), r.GPGKey)
	}
	for _, a := range vm.Artifacts {
		fmt.Fprintf(h, "artifact=%s/%s/%s/%t\n", a.Repo, a.Path, a.Digest, a.Extract)
	}
	if i := vm.Installer; i != nil {
		fmt.Fprintf(h, "installer=%s\n", i.Version)
		if f := i.ConfigFormatOr(); f != v1alpha1.ConfigFormatShell {
			fmt.Fprintf(h, "configFormat=%s\n", f)
		}
		for n, p := range i.Sequence() {
			fmt.Fprintf(h, "phase[%d]=%s/%s/%v/%v/%v/%s\n", n,
				p.Name, p.Script, p.Args, p.Steps, p.SkipSteps, derefStr(p.Timeout))
		}
	}
	if a := vm.Ansible; a != nil {
		fmt.Fprintf(h, "ansible=%s/%s/%s/%s\n", a.Repo, a.Path, a.Playbook, a.Version)
	}
	names := make([]string, 0, len(secrets))
	for k := range secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(h, "secret=%s=%s\n", k, secrets[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// VMInstallerVersion is the version an installation is reported in.
func VMInstallerVersion(vm *v1alpha1.VMSpec) string {
	if vm.Installer != nil {
		return vm.Installer.Version
	}
	if vm.Ansible != nil {
		return vm.Ansible.Version
	}
	return ""
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) bool { return p != nil && *p }

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
