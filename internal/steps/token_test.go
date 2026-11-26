package steps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAccess is a JFrog Platform serving the Access API at its root, Artifactory under
// /artifactory; it records the token requests.
type fakeAccess struct {
	mintStatus   int
	mintBody     string
	revokeStatus int

	mints   []*http.Request
	forms   []map[string]string
	revokes []string // Authorization headers of the revocations
	other   []string
}

func (f *fakeAccess) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/access/api/v1/tokens":
			_ = r.ParseForm()
			form := map[string]string{}
			for k := range r.PostForm {
				form[k] = r.PostForm.Get(k)
			}
			f.mints = append(f.mints, r)
			f.forms = append(f.forms, form)
			if f.mintStatus != 0 {
				w.WriteHeader(f.mintStatus)
				_, _ = w.Write([]byte(f.mintBody))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token_id":"id-1","access_token":"jwt-xyz","expires_in":0,` +
				`"scope":"applied-permissions/user","token_type":"Bearer"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/access/api/v1/tokens/me":
			f.revokes = append(f.revokes, r.Header.Get("Authorization"))
			if f.revokeStatus != 0 {
				w.WriteHeader(f.revokeStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			f.other = append(f.other, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMintDurableTokenThroughTheAccessAPI(t *testing.T) {
	f := &fakeAccess{}
	srv := f.serve(t)
	// The Access API sits at the platform root, beside the /artifactory context of the URL.
	a := &ArtifactoryClient{URL: srv.URL + "/artifactory/", User: "svc-rondo", Password: "p", HTTP: srv.Client()}

	tok, err := a.MintDurableToken(context.Background())
	if err != nil {
		t.Fatalf("MintDurableToken: %v", err)
	}
	if tok != "jwt-xyz" {
		t.Fatalf("token = %q, want jwt-xyz", tok)
	}
	if len(f.mints) != 1 || len(f.other) != 0 {
		t.Fatalf("mints %d, other requests %v: want one POST /access/api/v1/tokens", len(f.mints), f.other)
	}
	if user, pass, ok := f.mints[0].BasicAuth(); !ok || user != "svc-rondo" || pass != "p" {
		t.Errorf("basic auth = %q/%q (%v), want the mirror account", user, pass, ok)
	}
	for k, want := range map[string]string{
		"username": "svc-rondo", "scope": "applied-permissions/user", "expires_in": "0",
		"refreshable": "false",
	} {
		if got := f.forms[0][k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if f.forms[0]["description"] == "" {
		t.Error("no description: the token is not recognisable in the JFrog Platform")
	}
}

func TestShortLivedTokenExpiresInAnHour(t *testing.T) {
	f := &fakeAccess{}
	srv := f.serve(t)
	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p", HTTP: srv.Client()}
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := f.forms[0]["expires_in"]; got != "3600" {
		t.Errorf("expires_in = %q, want 3600", got)
	}
}

func TestRevokeTokenThroughTheAccessAPI(t *testing.T) {
	f := &fakeAccess{}
	srv := f.serve(t)
	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p", HTTP: srv.Client()}

	if err := a.RevokeToken(context.Background(), "jwt-old"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if len(f.revokes) != 1 || f.revokes[0] != "Bearer jwt-old" || len(f.other) != 0 {
		t.Errorf("revocations %v, other requests %v: want DELETE /access/api/v1/tokens/me "+
			"authenticated by the token", f.revokes, f.other)
	}

	if err := a.RevokeToken(context.Background(), ""); err != nil {
		t.Fatalf("RevokeToken(\"\"): %v", err)
	}
	if len(f.revokes) != 1 {
		t.Errorf("empty token triggered a revocation (%v)", f.revokes)
	}
}

func TestRevokeTokenStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		ok     bool
	}{
		"expired or already revoked": {http.StatusUnauthorized, true},
		"refused":                    {http.StatusForbidden, false},
		"server error":               {http.StatusInternalServerError, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAccess{revokeStatus: tc.status}
			srv := f.serve(t)
			a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p", HTTP: srv.Client()}
			err := a.RevokeToken(context.Background(), "jwt-old")
			if (err == nil) != tc.ok {
				t.Errorf("RevokeToken = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestMintDurableTokenExplainsTheAdminOnlyRefusal(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden} {
		f := &fakeAccess{mintStatus: status, mintBody: `{"errors":[{"status":` + "\n" +
			`"message":"Non-admin user cannot create token with no expiry"}]}`}
		srv := f.serve(t)
		a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "rondo-pipeline", Password: "p", HTTP: srv.Client()}
		_, err := a.MintDurableToken(context.Background())
		if err == nil {
			t.Fatal("MintDurableToken succeeded although the server refuses")
		}
		for _, want := range []string{
			"Non-admin user cannot create token with no expiry",
			"rondo-pipeline",
			"artifacts.artifactory.mirrorTokenLifetimeSeconds",
			"secrets.mirrorToken",
			"administrator",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("HTTP %d: error does not contain %q; got:\n%s", status, want, err)
			}
		}
	}
}

// A refused or missing Access API names secrets.mirrorToken, the way around minting.
func TestMintingRefusalPointsAtTheMirrorToken(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		f := &fakeAccess{mintStatus: status, mintBody: "denied"}
		srv := f.serve(t)
		a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "svc-rondo", Password: "p",
			HTTP: srv.Client(), DurableTokenTTL: "31536000"}
		for name, mint := range map[string]func(context.Context) (string, error){
			"durable": a.MintDurableToken, "short-lived": a.Token,
		} {
			_, err := mint(context.Background())
			if err == nil {
				t.Fatalf("HTTP %d %s: minting succeeded", status, name)
			}
			for _, want := range []string{"secrets.mirrorToken", "svc-rondo", "/access/api/v1/tokens", "denied"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("HTTP %d %s: error does not contain %q; got:\n%s", status, name, want, err)
				}
			}
		}
	}
}

func TestMintingFailureWithoutStatusIsReturned(t *testing.T) {
	f := &fakeAccess{}
	srv := f.serve(t)
	srv.Close()
	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p", HTTP: srv.Client()}
	if _, err := a.Token(context.Background()); err == nil || strings.Contains(err.Error(), "secrets.mirrorToken") {
		t.Errorf("error = %v, want the transport error alone", err)
	}
	if err := a.RevokeToken(context.Background(), "t"); err == nil {
		t.Error("revocation against a closed server succeeded")
	}
}

func TestMintedEmptyTokenIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_id":"x"}`))
	}))
	defer srv.Close()
	a := &ArtifactoryClient{URL: srv.URL, User: "u", Password: "p", HTTP: srv.Client(), DurableTokenTTL: "60"}
	if _, err := a.MintDurableToken(context.Background()); err == nil || !strings.Contains(err.Error(), "empty access_token") {
		t.Errorf("error = %v, want an empty access_token refusal", err)
	}
}

func TestMintDurableTokenHonoursTheConfiguredTTL(t *testing.T) {
	f := &fakeAccess{}
	srv := f.serve(t)
	a := &ArtifactoryClient{URL: srv.URL + "/artifactory", User: "u", Password: "p",
		HTTP: srv.Client(), DurableTokenTTL: "31536000"}
	if _, err := a.MintDurableToken(context.Background()); err != nil {
		t.Fatalf("MintDurableToken: %v", err)
	}
	if got := f.forms[0]["expires_in"]; got != "31536000" {
		t.Fatalf("expires_in = %q, want 31536000: the configured TTL was not sent", got)
	}
}
