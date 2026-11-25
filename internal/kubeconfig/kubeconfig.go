package kubeconfig

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

type doc struct {
	Clusters []struct {
		Name    string `json:"name"`
		Cluster struct {
			Server                   string `json:"server"`
			CertificateAuthorityData string `json:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `json:"insecure-skip-tls-verify"`
		} `json:"cluster"`
	} `json:"clusters"`
	Users []struct {
		Name string `json:"name"`
		User struct {
			Token                 string `json:"token"`
			ClientCertificateData string `json:"client-certificate-data"`
			ClientKeyData         string `json:"client-key-data"`
			Username              string `json:"username"`
			Password              string `json:"password"`
			Exec                  *struct {
				Command string `json:"command"`
			} `json:"exec"`
		} `json:"user"`
	} `json:"users"`
}

type Info struct {
	Server     string
	CAData     string
	Insecure   bool
	AuthMethod string
}

func Parse(raw []byte) (*Info, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty kubeconfig")
	}
	var d doc
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("unreadable kubeconfig: %w", err)
	}
	if len(d.Clusters) == 0 {
		return nil, fmt.Errorf("kubeconfig has no `clusters` entry")
	}
	cl := d.Clusters[0].Cluster
	if cl.Server == "" {
		return nil, fmt.Errorf("kubeconfig has no `server` (cluster %q)", d.Clusters[0].Name)
	}
	info := &Info{Server: cl.Server, CAData: cl.CertificateAuthorityData, Insecure: cl.InsecureSkipTLSVerify}

	if len(d.Users) == 0 {
		return nil, fmt.Errorf("kubeconfig has no `users` entry: no credentials to reach %s", cl.Server)
	}
	u := d.Users[0].User
	switch {
	case u.Token != "":
		info.AuthMethod = "token"
	case u.ClientCertificateData != "" && u.ClientKeyData != "":
		info.AuthMethod = "client certificate"
	case u.Username != "" && u.Password != "":
		info.AuthMethod = "basic"
	case u.Exec != nil && u.Exec.Command != "":
		return nil, fmt.Errorf("kubeconfig with a credential plugin (exec %q) is not supported air-gapped: "+
			"provide a token or a client certificate", u.Exec.Command)
	default:
		return nil, fmt.Errorf("kubeconfig has no credentials (no token, client certificate or basic auth) for %s", cl.Server)
	}

	if info.CAData == "" && !info.Insecure {
		return nil, fmt.Errorf("kubeconfig has neither `certificate-authority-data` nor "+
			"`insecure-skip-tls-verify`: the TLS connection to %s will fail. The CA must be "+
			"IN the kubeconfig", cl.Server)
	}
	return info, nil
}
