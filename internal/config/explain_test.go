package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestExplainPointsAtTheLine(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "rondo.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	example := string(raw)
	lineOfText := func(s, needle string) int {
		for i, l := range strings.Split(s, "\n") {
			if strings.Contains(l, needle) {
				return i + 1
			}
		}
		t.Fatalf("%q not found", needle)
		return 0
	}
	for name, tc := range map[string]struct {
		mutate func(string) string
		near   string
		path   string
		msg    string
	}{
		"syntax": {func(s string) string {
			return strings.Replace(s, "  username: svc-rondo", "  username: svc-rondo: x", 1)
		}, "username: svc-rondo", "", "YAML:"},
		"unknown field": {func(s string) string {
			return strings.Replace(s, "    vcenterAddress:", "    vcenterAdress:", 1)
		}, "vcenterAdress", "targets.vsphere[0].vcenterAdress", `unknown field "vcenterAdress"`},
		"rule inside a list": {func(s string) string {
			return strings.Replace(s, "gatewayIp: 10.0.0.1", "gatewayIp: gateway", 1)
		}, "gatewayIp", "targets.vsphere[0].vmNetwork.gatewayIp", "must be an IP address"},
		"missing required field": {func(s string) string {
			return strings.Replace(s, "      vmTemplate: rhel-9-template-cloudinit\n", "", 1)
		}, "- name: dc1", "targets.vsphere[0].vmTemplate", "value required"},
		"field inherited from Placement": {func(s string) string {
			return strings.Replace(s, "- name: rke2-a", "- name: RKE2_A", 1)
		}, "RKE2_A", "targets.downstreams[0].name", "must be a hostname"},
		"wrong type": {func(s string) string {
			return strings.Replace(s, "netmaskBits: 24", "netmaskBits: twenty", 1)
		}, "netmaskBits", "targets.vsphere.vmNetwork.netmaskBits", "expected type: int"},
		"path named by the rule": {func(s string) string {
			return strings.Replace(s, "    dc1: CHANGEME\n", "    dc1: CHANGEME\n    dc9: x\n", 1)
		}, "dc9: x", "secrets.vcenterPasswords.dc9", "dc9"},
		"target named by the rule": {func(s string) string {
			return strings.Replace(s, "[10.0.0.31, 10.0.0.32, 10.0.0.33]", "[10.0.0.31, 10.0.0.32]", 1)
		}, "- name: rke2-a", "", "odd"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := tc.mutate(example)
			_, err := Parse([]byte(bad))
			if err == nil {
				t.Fatal("manifest accepted")
			}
			p := Explain([]byte(bad), err)
			if want := lineOfText(bad, tc.near); p.Line != want {
				t.Errorf("line %d, want %d (%q); error: %v", p.Line, want, tc.near, err)
			}
			if p.Path != tc.path || !strings.Contains(p.Message, tc.msg) {
				t.Errorf("path %q, message %q; want %q, %q", p.Path, p.Message, tc.path, tc.msg)
			}
		})
	}
}

func TestExplainCountsTheOtherRules(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("..", "..", "rondo.yaml.example"))
	bad := strings.Replace(string(raw), "gatewayIp: 10.0.0.1", "gatewayIp: x", 1)
	bad = strings.Replace(bad, "stateURL: https://files.example.local/tofu-state", "stateURL: y", 1)
	_, err := Parse([]byte(bad))
	if p := Explain([]byte(bad), err); p.More != 1 {
		t.Errorf("%+v: want one more rule", p)
	}
}

type fieldError struct {
	validator.FieldError
	tag, param string
}

func (f fieldError) Tag() string   { return f.tag }
func (f fieldError) Param() string { return f.param }

func TestRuleMessagesAreReadable(t *testing.T) {
	for tag, want := range map[string]string{
		"required": "value required", "url": "must be a URL", "ip": "must be an IP address",
		"fqdn": "must be a fully qualified domain name", "hostname_rfc1123": "must be a hostname (RFC 1123)",
		"lowercase": "must be lowercase", "number": "must be a number",
		"excludes": `must not contain "p"`, "ne": `cannot be "p"`,
		"oneof": "one of: p", "min": "at least p", "max": "at most p", "len": "exactly p",
		"other": "rule other=p not satisfied",
	} {
		if got := ruleMessage(fieldError{tag: tag, param: "p"}); got != want {
			t.Errorf("%s: %q, want %q", tag, got, want)
		}
	}
}
