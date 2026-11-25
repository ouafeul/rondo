package httpx

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetBasicAuthAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "content")
	}))
	defer srv.Close()

	body, err := Get(context.Background(), srv.Client(), "user", "pass", srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	raw, _ := io.ReadAll(body)
	if string(raw) != "content" {
		t.Errorf("body = %q", raw)
	}
}

func TestGetAnonymousSkipsAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected Authorization header in anonymous mode")
		}
	}))
	defer srv.Close()
	body, err := Get(context.Background(), srv.Client(), "", "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
}

func TestGetNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := Get(context.Background(), srv.Client(), "", "", srv.URL); err == nil {
		t.Fatal("HTTP 404 accepted, want an error")
	}
}

func TestGetText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "abc123  iac.tar.gz\n")
	}))
	defer srv.Close()
	got, err := GetText(context.Background(), srv.Client(), "", "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123  iac.tar.gz\n" {
		t.Errorf("GetText = %q", got)
	}
}

func TestPostJSONBearerAndDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("bearer = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(map[string]string{"echo": in["msg"]})
	}))
	defer srv.Close()

	var out struct {
		Echo string `json:"echo"`
	}
	if err := PostJSON(context.Background(), srv.Client(), srv.URL, "tok", []byte(`{"msg":"hi"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "hi" {
		t.Errorf("echo = %q", out.Echo)
	}
}

func TestPostJSONNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	var out any
	if err := PostJSON(context.Background(), srv.Client(), srv.URL, "", nil, &out); err == nil {
		t.Fatal("HTTP 401 accepted, want an error")
	}
}

func TestPostFormEncodesAndAuths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		if u != "admin" || p != "secret" {
			t.Errorf("basic auth = %q/%q", u, p)
		}
		r.ParseForm()
		if r.PostForm.Get("scope") != "member-of-groups:*" {
			t.Errorf("scope = %q", r.PostForm.Get("scope"))
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	}))
	defer srv.Close()

	var out struct {
		AccessToken string `json:"access_token"`
	}
	form := url.Values{"scope": {"member-of-groups:*"}}
	if err := PostForm(context.Background(), srv.Client(), srv.URL, "admin", "secret", form, &out); err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "tok" {
		t.Errorf("token = %q", out.AccessToken)
	}
}

func TestNewCAPinnedRejectsBadPEM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCAPinned(p, time.Second); err == nil {
		t.Fatal("invalid PEM accepted")
	}
	if _, err := NewCAPinned(filepath.Join(t.TempDir(), "absent.pem"), time.Second); err == nil {
		t.Fatal("missing CA accepted")
	}
}

func TestNewCAPinnedTrustsServerCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	pemPath := filepath.Join(t.TempDir(), "ca.pem")
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(pemPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewCAPinned(pemPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	body, err := Get(context.Background(), client, "", "", srv.URL)
	if err != nil {
		t.Fatalf("CA-pinned client rejected the server: %v", err)
	}
	body.Close()

	plain := &http.Client{Timeout: 5 * time.Second}
	if _, err := Get(context.Background(), plain, "", "", srv.URL); err == nil {
		t.Fatal("self-signed server accepted without CA pinning")
	}
}
