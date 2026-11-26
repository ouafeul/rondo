package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestNewDefaultRefusesSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	if _, err := GetText(context.Background(), NewDefault(3*time.Second), "", "", srv.URL); err == nil {
		t.Error("the default client accepted a self-signed certificate")
	}
}
