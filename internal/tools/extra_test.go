package tools

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBinaryName(t *testing.T) {
	if (tool{name: "kubectl"}).binaryName() != "kubectl" {
		t.Error("binaryName without inner should equal name")
	}
	if (tool{name: "k9s", inner: "k9s-bin"}).binaryName() != "k9s-bin" {
		t.Error("binaryName with inner")
	}
}

func TestDef(t *testing.T) {
	if def(map[string]string{"K": "v"}, "K", "fb") != "v" {
		t.Error("def should prefer the env value")
	}
	if def(nil, "K", "fb") != "fb" {
		t.Error("def should fall back to the fallback")
	}
}

func TestExtractZipMissingBinary(t *testing.T) {
	data := zipWith(t, "other", "x")
	if err := extractZip(bytes.NewReader(data), "tofu", filepath.Join(t.TempDir(), "tofu")); err == nil {
		t.Fatal("binary missing from the zip should fail")
	}
}

func TestExtractZipNotAZip(t *testing.T) {
	if err := extractZip(bytes.NewReader([]byte("not a zip")), "x", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("non-zip data should fail")
	}
}

func TestExtractTarGzMissingBinary(t *testing.T) {
	data := targzWith(t, "other", "x")
	if err := extractTarGz(bytes.NewReader(data), "helm", filepath.Join(t.TempDir(), "helm")); err == nil {
		t.Fatal("binary missing from the tar.gz should fail")
	}
}

func TestExtractTarGzNotGzip(t *testing.T) {
	if err := extractTarGz(bytes.NewReader([]byte("not a gzip")), "x", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("non-gzip data should fail")
	}
}

func TestExtractZipWriteError(t *testing.T) {
	data := zipWith(t, "dir/tofu", "#!tofu")
	if err := extractZip(bytes.NewReader(data), "tofu", filepath.Join(t.TempDir(), "absent", "tofu")); err == nil {
		t.Fatal("writing to a missing directory should fail")
	}
}

func TestExtractTarGzWriteError(t *testing.T) {
	data := targzWith(t, "dir/helm", "#!helm")
	if err := extractTarGz(bytes.NewReader(data), "helm", filepath.Join(t.TempDir(), "absent", "helm")); err == nil {
		t.Fatal("writing to a missing directory should fail")
	}
}

func TestWriteExecBadDest(t *testing.T) {
	if err := writeExec(filepath.Join(t.TempDir(), "absent", "bin"), bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("invalid destination should fail")
	}
}

func TestFetchAndInstallUnknownKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("data"))
	}))
	defer srv.Close()
	in := &Installer{BaseURL: srv.URL, HTTP: srv.Client()}
	err := in.fetchAndInstall(context.Background(), tool{name: "x", path: "p", kind: "bogus"}, filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatal("unknown archive type should fail")
	}
}

func TestEachMkdirError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := writeExec(file, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	in := &Installer{BinDir: filepath.Join(file, "bin")}
	if err := in.Describe(context.Background()); err == nil {
		t.Fatal("invalid BinDir should fail")
	}
}
