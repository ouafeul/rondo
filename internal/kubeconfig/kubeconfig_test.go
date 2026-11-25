package kubeconfig

import (
	"strings"
	"testing"
)

func TestParseAcceptsUsableConfigs(t *testing.T) {
	cases := map[string]struct {
		doc        string
		wantAuth   string
		wantServer string
	}{
		"token + CA": {`
apiVersion: v1
kind: Config
clusters:
  - name: acme
    cluster: {server: "https://10.0.0.31:6443", certificate-authority-data: "Q0E="}
users:
  - name: acme
    user: {token: "eyJhbG"}`, "token", "https://10.0.0.31:6443"},

		"client certificate": {`
apiVersion: v1
kind: Config
clusters:
  - name: acme
    cluster: {server: "https://acme:6443", certificate-authority-data: "Q0E="}
users:
  - name: acme
    user: {client-certificate-data: "Q1JU", client-key-data: "S0VZ"}`, "client certificate", "https://acme:6443"},

		"insecure without CA": {`
apiVersion: v1
kind: Config
clusters:
  - name: sim
    cluster: {server: "https://mgmt:6443", insecure-skip-tls-verify: true}
users:
  - name: sim
    user: {token: "t"}`, "token", "https://mgmt:6443"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if info.Server != tc.wantServer {
				t.Errorf("Server = %q, want %q", info.Server, tc.wantServer)
			}
			if info.AuthMethod != tc.wantAuth {
				t.Errorf("AuthMethod = %q, want %q", info.AuthMethod, tc.wantAuth)
			}
		})
	}
}

func TestParseRefusesExecPlugin(t *testing.T) {
	_, err := Parse([]byte(`
apiVersion: v1
kind: Config
clusters:
  - name: c
    cluster: {server: "https://x:6443", certificate-authority-data: "Q0E="}
users:
  - name: c
    user:
      exec:
        command: aws-iam-authenticator`))
	if err == nil {
		t.Fatal("credential plugin accepted: the Job would fail on a missing exec")
	}
	for _, want := range []string{"aws-iam-authenticator", "air-gap", "token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, must contain %q", err, want)
		}
	}
}

func TestParseRefusesUnusableConfigs(t *testing.T) {
	cases := map[string]struct {
		doc         string
		wantInError string
	}{
		"empty": {"", "empty"},

		"unreadable": {"{this is not: valid: yaml", "unreadable"},

		"no clusters": {`
apiVersion: v1
kind: Config
users:
  - name: u
    user: {token: t}`, "clusters"},

		"no server": {`
apiVersion: v1
kind: Config
clusters:
  - name: empty
    cluster: {certificate-authority-data: "Q0E="}
users:
  - name: u
    user: {token: t}`, "server"},

		"no users": {`
apiVersion: v1
kind: Config
clusters:
  - name: c
    cluster: {server: "https://x:6443", certificate-authority-data: "Q0E="}`, "credentials"},

		"users without credentials": {`
apiVersion: v1
kind: Config
clusters:
  - name: c
    cluster: {server: "https://x:6443", certificate-authority-data: "Q0E="}
users:
  - name: c
    user: {}`, "no credentials"},

		"neither CA nor insecure": {`
apiVersion: v1
kind: Config
clusters:
  - name: c
    cluster: {server: "https://x:6443"}
users:
  - name: c
    user: {token: t}`, "certificate-authority-data"},

		"incomplete client certificate": {`
apiVersion: v1
kind: Config
clusters:
  - name: c
    cluster: {server: "https://x:6443", certificate-authority-data: "Q0E="}
users:
  - name: c
    user: {client-key-data: "S0VZ"}`, "no credentials"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatalf("unusable kubeconfig accepted")
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("message = %q, must contain %q", err, tc.wantInError)
			}
		})
	}
}

func TestParseNeverLeaksCredentials(t *testing.T) {
	const secret = "SUPER-SECRET-TOKEN-abc123"
	_, err := Parse([]byte(`
apiVersion: v1
kind: Config
clusters:
  - name: c
    cluster: {server: "https://x:6443"}
users:
  - name: c
    user: {token: "` + secret + `"}`))
	if err == nil {
		t.Fatal("want an error (neither CA nor insecure)")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error message contains the token: %q", err)
	}
}
