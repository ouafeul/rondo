package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func TestExtractVerified_OK(t *testing.T) {
	data, sum := makeTarGz(t, map[string]string{
		"manifests/dependencies.yaml": "binaries: []\n",
		"helmfile.yaml":               "releases: []\n",
	})
	dir := t.TempDir()
	if err := extractVerified(bytes.NewReader(data), sum, dir); err != nil {
		t.Fatalf("extractVerified: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "manifests", "dependencies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binaries: []\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestExtractVerified_ChecksumMismatch(t *testing.T) {
	data, _ := makeTarGz(t, map[string]string{"a.txt": "x"})
	if err := extractVerified(bytes.NewReader(data), "deadbeef", t.TempDir()); err == nil {
		t.Fatal("want a checksum error, got nil")
	}
}

func TestWriteEntry_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	h := &tar.Header{Name: "../escape.txt", Mode: 0o644, Typeflag: tar.TypeReg}
	if err := writeEntry(dir, h, bytes.NewReader([]byte("pwn"))); err == nil {
		t.Fatal("want a path traversal rejection, got nil")
	}
}

func TestSHA256Field(t *testing.T) {
	const d = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	for in, want := range map[string]string{
		d + "  iac.tar.gz\n":         d, // sha256sum line, the sidecar the mirror scripts write
		d:                            d, // bare digest, what Artifactory answers for <file>.sha256
		"  " + d + " \n":             d,
		strings.ToUpper(d) + "\n":    d,
		d + " *iac.tar.gz\r\n":       d,
		"":                           "",
		"deadbeef  iac.tar.gz\n":     "",
		"<html>not found</html>":     "",
		strings.Repeat("z", 64) + "": "",
	} {
		got, err := sha256Field(in)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("sha256Field(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
