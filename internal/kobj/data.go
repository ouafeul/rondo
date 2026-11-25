package kobj

import (
	"fmt"
	"strings"
)

func OpaqueSecret(name string, data map[string]string) Object {
	return secret(name, PlatformNamespace, "Opaque", data)
}

func secret(name, ns, typ string, data map[string]string) Object {
	return Object{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   Object{"name": name, "namespace": ns},
		"type":       typ,
		"stringData": toAnyMap(data),
	}
}

func configMap(name, ns string, data map[string]string) Object {
	return Object{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   Object{"name": name, "namespace": ns},
		"data":       toAnyMap(data),
	}
}

// MirrorCredentials is read as env by every Job, through envFrom: the mirror's credentials,
// both empty for an anonymous mirror, and the keys of an s3 state backend when there is one,
// under the names OpenTofu's s3 backend reads.
func MirrorCredentials(user, password, stateAccessKey, stateSecretKey string) Object {
	data := map[string]string{
		CredentialsUserKey:     user,
		CredentialsPasswordKey: password,
	}
	if stateAccessKey != "" {
		data[StateAccessKeyEnv] = stateAccessKey
		data[StateSecretKeyEnv] = stateSecretKey
	}
	return OpaqueSecret(CredentialsSecret, data)
}

// MirrorCA holds the CA bundle of the mirror, empty when the system trust store is used.
func MirrorCA(pem string) Object {
	return configMap(MirrorCACM, PlatformNamespace, map[string]string{MirrorCAKey: pem})
}

// TofuMirror holds the .tofurc that points OpenTofu at the provider mirror (config.Mirror.TofuRC).
// storeToken also keeps the bare token, so a later run can revoke what it minted.
func TofuMirror(tofurc, token string, storeToken bool) Object {
	data := map[string]string{TofurcKey: tofurc}
	if storeToken {
		data[TofurcTokenKey] = token
	}
	return OpaqueSecret(TofurcSecret, data)
}

func KubeconfigSecret(target, kubeconfig string) Object {
	return OpaqueSecret(KubeconfigSecretName(target), map[string]string{KubeconfigKey: kubeconfig})
}

func VSphereCredentials(target, user, password string) Object {
	return OpaqueSecret(VSphereObjectName(target), map[string]string{
		VSphereUserKey:     user,
		VSpherePasswordKey: password,
	})
}

func VSphereSettings(target string, settings map[string]string) Object {
	return configMap(VSphereObjectName(target), PlatformNamespace, settings)
}

func VMSSHKey(private, public string) Object {
	return OpaqueSecret(VMSSHSecret, map[string]string{
		VMSSHPrivateKey: private,
		VMSSHPublicKey:  public,
	})
}

func Regcred(user, password string) Object {
	return secret(RegcredName, RegcredNamespace, "kubernetes.io/basic-auth",
		map[string]string{"username": user, "password": password})
}

// CoreDNSCustom is the ConfigMap k3s's CoreDNS imports its extra server blocks from.
func CoreDNSCustom(override string) Object {
	return configMap(CoreDNSConfigMap, CoreDNSNamespace, map[string]string{CoreDNSOverrideKey: override})
}

// CoreDNSTemplate answers match with ip. `template`, not `hosts`: k3s already has a `hosts` in
// .:53 and a second one is inert.
func CoreDNSTemplate(match, ip string) string {
	return fmt.Sprintf(
		"template IN A {\n  match %s\n  answer \"{{ .Name }} 60 IN A %s\"\n  fallthrough\n}\n",
		match, ip)
}

// ComponentRecord remembers what a tree applied, one Kind/Name per line, for the next prune.
func ComponentRecord(tree, version string, objects []string) Object {
	return configMap(RecordName(tree), PlatformNamespace, map[string]string{
		"version": version,
		"objects": strings.Join(objects, "\n"),
	})
}
