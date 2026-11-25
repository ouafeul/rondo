package config

import (
	"fmt"
	"strings"
)

// ProviderSource is the OpenTofu registry the modules' providers resolve to (hashicorp/vsphere
// is registry.opentofu.org/hashicorp/vsphere), the one an OCI mirror stands in for.
const ProviderSource = "registry.opentofu.org"

// TofuRC is the OpenTofu CLI configuration that installs the providers from the mirror: a
// network_mirror, with token as the credentials of its host when set, or an oci_mirror, with
// the mirror's username and password when it has some. OpenTofu 1.10 or later reads oci_mirror.
func (m Mirror) TofuRC(token string) string {
	var b strings.Builder
	if m.ProviderMirrorURL == "" && m.ProviderOCI != "" {
		fmt.Fprintf(&b, "provider_installation {\n  oci_mirror {\n"+
			"    repository_template = \"%s/${namespace}/${type}\"\n"+
			"    include             = [\"%s/*/*\"]\n  }\n}\n", hclEscape(m.ProviderOCI), ProviderSource)
		if !m.Anonymous() {
			fmt.Fprintf(&b, "oci_credentials %q {\n  username = %s\n  password = %s\n}\n",
				m.ProviderMirrorHost(), hclQuote(m.Username), hclQuote(m.Password))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "provider_installation {\n  network_mirror {\n    url = %s\n  }\n}\n",
		hclQuote(m.ProviderMirrorURL))
	if token != "" {
		fmt.Fprintf(&b, "credentials %s {\n  token = %s\n}\n", hclQuote(m.ProviderMirrorHost()), hclQuote(token))
	}
	return b.String()
}

// hclQuote is s as an HCL string literal.
func hclQuote(s string) string { return `"` + hclEscape(s) + `"` }

// hclEscape escapes s for the inside of an HCL string literal, template sequences included.
func hclEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "${", "$${", "%{", "%%{").Replace(s)
}
