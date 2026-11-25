package config

import (
	"strings"
	"testing"

	"rondo/internal/tofustate"
)

// anonymous is the example without credentials nor CA.
func anonymous(s string) string {
	s = strings.Replace(s, "  username: svc-rondo\n", "", 1)
	s = strings.Replace(s, "  caCertificateFile: ./certs/mirror-ca.pem\n", "", 1)
	return strings.Replace(s, "  mirrorPassword: CHANGEME\n", "", 1)
}

func TestGenericPresetReadAnonymouslyWithoutCA(t *testing.T) {
	c, err := load(t, anonymous)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Mirror()
	if !m.Anonymous() || m.CAFile != "" {
		t.Fatalf("mirror = %+v, want anonymous without CA", m)
	}
	if auth, err := m.FilesAuthURL(); err != nil || auth != "https://files.example.local" {
		t.Errorf("files URL = %q, %v; want no userinfo", auth, err)
	}
	vars := c.RKE2Vars(&c.Targets.Downstreams[0])
	if vars["rke2_registry_auth"] != "false" {
		t.Errorf("rke2_registry_auth = %q, want false: no regcred to point at", vars["rke2_registry_auth"])
	}
	if rc := m.TofuRC(""); strings.Contains(rc, "credentials") {
		t.Errorf(".tofurc with credentials for an anonymous mirror:\n%s", rc)
	}
}

func TestCredentialsSetMakeTheRegistryAuthenticated(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := c.RKE2Vars(&c.Targets.Downstreams[0])["rke2_registry_auth"]; v != "true" {
		t.Errorf("rke2_registry_auth = %q, want true", v)
	}
}

// ociS3 is the example with its providers in an OCI mirror and its states in S3.
func ociS3(s string) string {
	s = strings.Replace(s, "    providerMirror: https://files.example.local/tofu-providers/\n",
		"    providerOCI: registry.example.local/opentofu-providers/\n", 1)
	return strings.Replace(s, "      http:\n        url: https://files.example.local/tofu-state\n",
		"      s3:\n        bucket: rondo-state\n        endpoint: https://minio.example.local:9000/\n"+
			"        keyPrefix: /sites/\n        usePathStyle: true\n", 1)
}

func TestGenericPresetWithOCIProvidersAndS3State(t *testing.T) {
	c, err := load(t, ociS3)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Mirror()
	if m.ProviderMirrorURL != "" || m.ProviderOCI != "registry.example.local/opentofu-providers" ||
		m.ProviderMirrorHost() != "registry.example.local" {
		t.Errorf("providers = %q / %q (host %q)", m.ProviderMirrorURL, m.ProviderOCI, m.ProviderMirrorHost())
	}
	want := tofustate.S3Bucket{Bucket: "rondo-state", Endpoint: "https://minio.example.local:9000",
		Region: "us-east-1", KeyPrefix: "sites/prod", UsePathStyle: true}
	if m.StateBackend != tofustate.S3 || m.StateS3 != want {
		t.Errorf("state = %s %+v, want s3 %+v", m.StateBackend, m.StateS3, want)
	}
	if m.StateBaseURL() != "" {
		t.Errorf("http state base %q with an s3 backend", m.StateBaseURL())
	}
	if got := m.StateS3.Key("k3s"); got != "sites/prod/k3s/terraform.tfstate" {
		t.Errorf("k3s key = %q", got)
	}
	// Without keys of their own, the shared credentials authenticate on the bucket.
	if m.StateAccessKey != "svc-rondo" || m.StateSecretKey != "CHANGEME" {
		t.Errorf("state keys = %q / %q, want the mirror's", m.StateAccessKey, m.StateSecretKey)
	}
}

func TestS3StateTakesItsOwnKeys(t *testing.T) {
	c, err := load(t, func(s string) string {
		return strings.Replace(anonymous(ociS3(s)), "secrets:\n",
			"secrets:\n  mirrorStateAccessKey: AK\n  mirrorStateSecretKey: SK\n", 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Mirror(); m.StateAccessKey != "AK" || m.StateSecretKey != "SK" || !m.Anonymous() {
		t.Errorf("mirror = %+v, want an anonymous mirror and the state's own keys", m)
	}
}

func TestTofuRCForAnOCIMirror(t *testing.T) {
	c, err := load(t, ociS3)
	if err != nil {
		t.Fatal(err)
	}
	want := `provider_installation {
  oci_mirror {
    repository_template = "registry.example.local/opentofu-providers/${namespace}/${type}"
    include             = ["registry.opentofu.org/*/*"]
  }
}
oci_credentials "registry.example.local" {
  username = "svc-rondo"
  password = "CHANGEME"
}
`
	if got := c.Mirror().TofuRC("ignored"); got != want {
		t.Errorf(".tofurc:\n%s\nwant:\n%s", got, want)
	}
}

func TestTofuRCForANetworkMirror(t *testing.T) {
	m := Mirror{ProviderMirrorURL: "https://files.example.local/providers/"}
	want := "provider_installation {\n  network_mirror {\n    url = \"https://files.example.local/providers/\"\n  }\n}\n" +
		"credentials \"files.example.local\" {\n  token = \"t\\\"k\"\n}\n"
	if got := m.TofuRC(`t"k`); got != want {
		t.Errorf(".tofurc:\n%s\nwant:\n%s", got, want)
	}
	if got := m.TofuRC(""); strings.Contains(got, "credentials") {
		t.Errorf("credentials without a token:\n%s", got)
	}
}

func TestHCLEscapesTemplates(t *testing.T) {
	if got := hclQuote(`p${x}%{y}\`); got != `"p$${x}%%{y}\\"` {
		t.Errorf("hclQuote = %s", got)
	}
}
