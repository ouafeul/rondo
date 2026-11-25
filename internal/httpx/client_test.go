package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestNewDefaultEnforcesTLS12(t *testing.T) {
	c := NewDefault(3 * time.Second)
	if c.Timeout != 3*time.Second {
		t.Errorf("timeout = %v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", c.Transport)
	}
	if tr.TLSClientConfig.MinVersion != 0x0303 {
		t.Errorf("MinVersion = %x, want TLS 1.2", tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("NewDefault skips certificate verification")
	}
}

func TestPostFormRawToleratesTextBody(t *testing.T) {
	var gotForm, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotForm = string(b)
		gotUser, _, _ = r.BasicAuth()
		_, _ = w.Write([]byte("Token revoked"))
	}))
	defer srv.Close()

	form := url.Values{"token": {"tok-abc"}}
	if err := PostFormRaw(context.Background(), srv.Client(), srv.URL, "svc", "pw", form); err != nil {
		t.Fatalf("PostFormRaw: %v", err)
	}
	if gotForm != "token=tok-abc" {
		t.Errorf("form = %q", gotForm)
	}
	if gotUser != "svc" {
		t.Errorf("user = %q", gotUser)
	}
}

func TestPostFormRawRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if err := PostFormRaw(context.Background(), srv.Client(), srv.URL, "u", "p",
		url.Values{}); err == nil {
		t.Fatal("401 accepted")
	}
	if err := PostFormRaw(context.Background(), http.DefaultClient, "://x", "u", "p",
		url.Values{}); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

func TestNewDefaultRefusesSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	if _, err := GetText(context.Background(), NewDefault(3*time.Second), "", "", srv.URL); err == nil {
		t.Error("the default client accepted a self-signed certificate")
	}
}
