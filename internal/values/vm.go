package values

import (
	"fmt"
	"sort"
	"strings"

	"rondo/api/v1alpha1"
)

const secretPrefix = "secrets."

type SecretsInVMError struct {
	Tokens []string
}

func (e *SecretsInVMError) Error() string {
	return fmt.Sprintf("secret token(s) not allowed in spec.vm: %s; the "+
		"configuration is rendered as 0640 and STAYS on the machine. Declare the key "+
		"in `installer.secretsFrom` and read it from `installer.secretsFile` "+
		"(0600, shredded after installation)",
		strings.Join(e.Tokens, ", "))
}

func ResolveVM(vm *v1alpha1.VMSpec, vars Vars) (*v1alpha1.VMSpec, error) {
	if vm == nil {
		return nil, nil
	}
	out := vm.DeepCopy()
	var missing, secrets []string
	out.Template = substitute(out.Template, vars, &missing, &secrets)
	resolveVMConfig(out.Config, vars, &missing, &secrets)
	for i := range out.Groups {
		resolveVMConfig(out.Groups[i].Config, vars, &missing, &secrets)
	}
	if len(secrets) > 0 {
		sort.Strings(secrets)
		return nil, &SecretsInVMError{Tokens: dedupe(secrets)}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &MissingVarsError{Tokens: dedupe(missing), Known: knownKeys(vars)}
	}
	return out, nil
}

func resolveVMConfig(cfg map[string]v1alpha1.VMConfigValue, vars Vars, missing, secrets *[]string) {
	for k, v := range cfg {
		if v.Value != nil {
			s := substitute(*v.Value, vars, missing, secrets)
			v.Value = &s
		}
		for i, item := range v.List {
			v.List[i] = substitute(item, vars, missing, secrets)
		}
		cfg[k] = v
	}
}
