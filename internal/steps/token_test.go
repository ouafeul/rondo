package steps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintDurableAndRevokeToken(t *testing.T) {
	var mintPath, revokePath, expires, revokedToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/artifactory/api/security/token":
			mintPath = r.URL.Path
			expires = r.FormValue("expires_in")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"jwt-xyz","token_type":"Bearer","expires_in":0}`))
		case "/artifactory/api/security/token/revoke":
			revokePath = r.URL.Path
			revokedToken = r.FormValue("token")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("token revoked"))
		}
	}))
	defer srv.Close()

	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p", HTTP: srv.Client()}

	tok, err := a.MintDurableToken(context.Background())
	if err != nil {
		t.Fatalf("MintDurableToken: %v", err)
	}
	if tok != "jwt-xyz" {
		t.Fatalf("token = %q, want jwt-xyz", tok)
	}
	if mintPath != "/artifactory/api/security/token" {
		t.Errorf("mint path = %q", mintPath)
	}
	if expires != "0" {
		t.Errorf("expires_in = %q, want 0 (non-expiring)", expires)
	}

	if err := a.RevokeToken(context.Background(), "jwt-old"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if revokePath != "/artifactory/api/security/token/revoke" || revokedToken != "jwt-old" {
		t.Errorf("revoke path=%q token=%q", revokePath, revokedToken)
	}

	revokePath = ""
	if err := a.RevokeToken(context.Background(), ""); err != nil {
		t.Fatalf("RevokeToken(\"\"): %v", err)
	}
	if revokePath != "" {
		t.Errorf("empty token triggered a revoke (%q)", revokePath)
	}
}

func TestMintDurableTokenExplainsTheAdminOnlyRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"status":400,` + "\n" +
			`"message":"Non-admin user cannot create token with no expiry"}]}`))
	}))
	defer srv.Close()

	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "rondo-pipeline", Password: "p", HTTP: srv.Client()}
	_, err := a.MintDurableToken(context.Background())
	if err == nil {
		t.Fatal("MintDurableToken succeeded although the server refuses")
	}
	for _, want := range []string{
		"Non-admin user cannot create token with no expiry",
		"rondo-pipeline",
		"durableTokenTtl",
		"mirrorToken",
		"administrator",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q; got:\n%s", want, err)
		}
	}
}

func TestMintDurableTokenHonoursTheConfiguredTTL(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.FormValue("expires_in")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"jwt-finite"}`))
	}))
	defer srv.Close()

	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p",
		HTTP: srv.Client(), DurableTokenTTL: "31536000"}
	if _, err := a.MintDurableToken(context.Background()); err != nil {
		t.Fatalf("MintDurableToken: %v", err)
	}
	if got != "31536000" {
		t.Fatalf("expires_in = %q, want 31536000: the configured TTL was not sent", got)
	}
}
