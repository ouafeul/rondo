package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func multiTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractTarGzTree(t *testing.T) {
	data := multiTarGz(t, map[string]string{
		"diff/plugin.yaml": "name: diff\n",
		"diff/bin/diff":    "#!binary",
	})
	root := t.TempDir()
	if err := extractTarGzTree(bytes.NewReader(data), root); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "diff", "plugin.yaml")); err != nil || string(raw) != "name: diff\n" {
		t.Errorf("plugin.yaml: %q err=%v", raw, err)
	}
	fi, err := os.Stat(filepath.Join(root, "diff", "bin", "diff"))
	if err != nil {
		t.Fatalf("bin/diff missing: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("bin/diff not executable: %v", fi.Mode())
	}
}

func TestExtractTarGzTreeZipSlip(t *testing.T) {
	data := multiTarGz(t, map[string]string{"../evil": "x"})
	if err := extractTarGzTree(bytes.NewReader(data), t.TempDir()); err == nil {
		t.Fatal("entry outside the target directory accepted (zip-slip)")
	}
}

func TestInstallHelmDiff(t *testing.T) {
	archive := multiTarGz(t, map[string]string{
		"diff/plugin.yaml": "name: diff\n",
		"diff/bin/diff":    "#!binary",
	})
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helm-diff/v3.15.15/helm-diff-linux-amd64.tgz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits++
		w.Write(archive)
	}))
	defer srv.Close()

	pluginDir := t.TempDir()
	in := &Installer{BaseURL: srv.URL, User: "u", Password: "p", HTTP: srv.Client(), PluginDir: pluginDir, Versions: nil}
	if err := in.installHelmDiff(context.Background()); err != nil {
		t.Fatalf("installHelmDiff: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "diff", "plugin.yaml")); err != nil {
		t.Errorf("plugin not installed: %v", err)
	}
	if err := in.installHelmDiff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("downloads = %d, want 1 (idempotent)", hits)
	}
}

func TestInstallHelmDiffNoPluginDir(t *testing.T) {
	in := &Installer{BaseURL: "https://example.invalid", Versions: nil}
	if err := in.installHelmDiff(context.Background()); err != nil {
		t.Fatalf("want a no-op: %v", err)
	}
}

func TestInstallHelmDiffVersionFromManifest(t *testing.T) {
	archive := multiTarGz(t, map[string]string{"diff/plugin.yaml": "n\n"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helm-diff/v9.9.9/helm-diff-linux-amd64.tgz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()
	in := &Installer{BaseURL: srv.URL, HTTP: srv.Client(), PluginDir: t.TempDir(),
		Versions: map[string]string{"HELM_DIFF_VERSION": "v9.9.9"}}
	if err := in.installHelmDiff(context.Background()); err != nil {
		t.Fatalf("manifest version ignored: %v", err)
	}
}
