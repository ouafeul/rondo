package constructs

type PKIStack struct {
	CommonName      string `validate:"required"`
	RancherHostname string `validate:"required,fqdn"`
	CADuration      string `validate:"omitempty"`
}

func (p *PKIStack) ComponentName() string { return "cluster-pki" }

func (p *PKIStack) Validate() error { return validate.Struct(p) }

func (p *PKIStack) Manifests() ([]Object, error) {
	dur := p.CADuration
	if dur == "" {
		dur = "87600h"
	}
	labels := stdLabels(p.ComponentName(), "PKIStack")

	selfSigned := Object{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   Object{"name": "selfsigned-bootstrap", "labels": labels},
		"spec":       Object{"selfSigned": Object{}},
	}
	clusterCA := Object{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   Object{"name": "cluster-ca", "namespace": "cert-manager", "labels": labels},
		"spec": Object{
			"isCA":       true,
			"commonName": p.CommonName,
			"secretName": "cluster-ca-keypair",
			"duration":   dur,
			"privateKey": Object{"algorithm": "RSA", "size": 4096, "rotationPolicy": "Never"},
			"issuerRef":  Object{"name": "selfsigned-bootstrap", "kind": "ClusterIssuer"},
		},
	}
	caIssuer := Object{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   Object{"name": "cluster-ca-issuer", "labels": labels},
		"spec":       Object{"ca": Object{"secretName": "cluster-ca-keypair"}},
	}
	rancherTLS := Object{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   Object{"name": "rancher-tls", "namespace": "cattle-system", "labels": labels},
		"spec": Object{
			"secretName": "tls-rancher-ingress",
			"issuerRef":  Object{"name": "cluster-ca-issuer", "kind": "ClusterIssuer"},
			"dnsNames":   []any{p.RancherHostname},
		},
	}
	return []Object{selfSigned, clusterCA, caIssuer, rancherTLS}, nil
}
