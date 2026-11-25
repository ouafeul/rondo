package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewInsecureSkipsVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewInsecure(5 * time.Second)
	body, err := Get(context.Background(), c, "", "", srv.URL)
	if err != nil {
		t.Fatalf("insecure client rejected the self-signed server: %v", err)
	}
	body.Close()
}

func TestGetTextNetworkError(t *testing.T) {
	if _, err := GetText(context.Background(), http.DefaultClient, "", "", "http://127.0.0.1:0"); err == nil {
		t.Fatal("want a network error")
	}
}

func TestPostJSONNetworkError(t *testing.T) {
	var out any
	if err := PostJSON(context.Background(), http.DefaultClient, "http://127.0.0.1:0", "", []byte(`{}`), &out); err == nil {
		t.Fatal("want a network error (PostJSON)")
	}
}

func TestPostFormNetworkError(t *testing.T) {
	var out any
	if err := PostForm(context.Background(), http.DefaultClient, "http://127.0.0.1:0", "u", "p", nil, &out); err == nil {
		t.Fatal("want a network error (PostForm)")
	}
}

func TestBadURLRequestConstruction(t *testing.T) {
	bad := "://not-a-url"
	if _, err := Get(context.Background(), http.DefaultClient, "", "", bad); err == nil {
		t.Error("Get should fail on an invalid URL")
	}
	if _, err := GetText(context.Background(), http.DefaultClient, "", "", bad); err == nil {
		t.Error("GetText should fail on an invalid URL")
	}
	var out any
	if err := PostJSON(context.Background(), http.DefaultClient, bad, "", nil, &out); err == nil {
		t.Error("PostJSON should fail on an invalid URL")
	}
	if err := PostForm(context.Background(), http.DefaultClient, bad, "u", "p", nil, &out); err == nil {
		t.Error("PostForm should fail on an invalid URL")
	}
}

func TestGetTextOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := GetText(context.Background(), srv.Client(), "", "", srv.URL); err == nil {
		t.Fatal("HTTP 500 accepted")
	}
}
