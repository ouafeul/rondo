package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolsetVersionsFromEnv(t *testing.T) {
	ts := toolset(map[string]string{"TOFU_VERSION": "9.9.9"})
	if !strings.Contains(ts[0].path, "9.9.9") {
		t.Errorf("env version ignored: %s", ts[0].path)
	}
	if !strings.Contains(toolset(nil)[1].path, "helm-v") {
		t.Errorf("helm fallback missed: %s", toolset(nil)[1].path)
	}
}

func zipWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(body))
	zw.Close()
	return buf.Bytes()
}

func targzWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallAllKinds(t *testing.T) {
	payloads := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "user" || p != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := payloads[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	versions := map[string]string{}
	for _, tl := range toolset(versions) {
		switch tl.kind {
		case "raw":
			payloads["/"+tl.path] = []byte("#!" + tl.name)
		case "zip":
			payloads["/"+tl.path] = zipWith(t, "dir/"+tl.binaryName(), "#!"+tl.name)
		case "targz":
			payloads["/"+tl.path] = targzWith(t, "dir/"+tl.binaryName(), "#!"+tl.name)
		}
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "yq"), []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	in := &Installer{BaseURL: srv.URL, User: "user", Password: "pass", HTTP: srv.Client(), BinDir: binDir, Versions: versions}
	if err := in.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	for _, name := range []string{"tofu", "helm", "helmfile", "kubectl", "k9s"} {
		raw, err := os.ReadFile(filepath.Join(binDir, name))
		if err != nil {
			t.Fatalf("%s not installed: %v", name, err)
		}
		if string(raw) != "#!"+name {
			t.Errorf("%s: content %q", name, raw)
		}
		fi, _ := os.Stat(filepath.Join(binDir, name))
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s not executable: %v", name, fi.Mode())
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(binDir, "yq")); string(raw) != "already here" {
		t.Error("pre-installed yq overwritten")
	}
}

func TestInstallHTTPErrorWrapsToolName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	in := &Installer{BaseURL: srv.URL, HTTP: srv.Client(), BinDir: t.TempDir(), Versions: nil}
	err := in.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tool tofu") {
		t.Fatalf("want an error naming the tool, got %v", err)
	}
}

func TestDescribeNoNetwork(t *testing.T) {
	in := &Installer{BaseURL: "https://example.invalid/repo", BinDir: t.TempDir(), Versions: nil}
	if err := in.Describe(context.Background()); err != nil {
		t.Fatalf("Describe: %v", err)
	}
}
