package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rondo/internal/config"
)

func bundleSrv(t *testing.T, routes map[string][]byte) (*httptest.Server, *config.Config) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				w.Write(body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	c := &config.Config{
		Artifacts: config.Artifacts{Preset: config.PresetArtifactory, Username: "u",
			Artifactory: config.ArtifactoryMirror{URL: srv.URL + "/artifactory",
				Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}},
		Secrets: config.Secrets{MirrorPassword: "p"},
	}
	return srv, c
}

func TestResolvePinnedDownloadsAndCaches(t *testing.T) {
	data, sum := makeTarGz(t, map[string]string{"helmfile.yaml": "releases: []\n"})
	srv, c := bundleSrv(t, map[string][]byte{
		"/v1.0.0/iac.tar.gz.sha256": []byte(sum + "  iac.tar.gz\n"),
		"/v1.0.0/iac.tar.gz":        data,
	})
	defer srv.Close()
	cache := t.TempDir()

	dir, err := Resolve(context.Background(), srv.Client(), c, cache, "stable", "v1.0.0")
	if err != nil {
		t.Fatalf("Resolve pinned: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "helmfile.yaml")); string(got) != "releases: []\n" {
		t.Errorf("archive not extracted: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".rondo-bundle-ok")); err != nil {
		t.Errorf("cache marker missing: %v", err)
	}

	srv.Close()
	dir2, err := Resolve(context.Background(), srv.Client(), c, cache, "stable", "v1.0.0")
	if err != nil || dir2 != dir {
		t.Errorf("cache not reused: %q %v", dir2, err)
	}
}

func TestResolveViaChannelPointer(t *testing.T) {
	data, sum := makeTarGz(t, map[string]string{"manifests/dependencies.yaml": "binaries: []\n"})
	srv, c := bundleSrv(t, map[string][]byte{
		"/stable.json":              []byte(`{"version":"v1.3.4"}`),
		"/v1.3.4/iac.tar.gz.sha256": []byte(sum),
		"/v1.3.4/iac.tar.gz":        data,
	})
	defer srv.Close()

	dir, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("Resolve via channel: %v", err)
	}
	if filepath.Base(dir) != "v1.3.4" {
		t.Errorf("unexpected resolved version: %q", dir)
	}
}

func TestResolveViaEdgeChannelPointer(t *testing.T) {
	data, sum := makeTarGz(t, map[string]string{"helmfile.yaml": "releases: []\n"})
	const edgeVer = "edge-feat-example-9caec7a"
	srv, c := bundleSrv(t, map[string][]byte{
		"/edge-feat-example.json":            []byte(`{"version":"` + edgeVer + `"}`),
		"/stable.json":                       []byte(`{"version":"v0.9.0"}`),
		"/" + edgeVer + "/iac.tar.gz.sha256": []byte(sum + "  iac.tar.gz\n"),
		"/" + edgeVer + "/iac.tar.gz":        data,
	})
	defer srv.Close()

	dir, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "edge-feat-example", "")
	if err != nil {
		t.Fatalf("Resolve via edge channel: %v", err)
	}
	if filepath.Base(dir) != edgeVer {
		t.Errorf("resolved version = %q, want %q (was the stable pointer read?)", filepath.Base(dir), edgeVer)
	}
}

func recordingSrv(t *testing.T, routes map[string][]byte) (*httptest.Server, *config.Config, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				w.Write(body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	c := &config.Config{
		Artifacts: config.Artifacts{Preset: config.PresetArtifactory, Username: "u",
			Artifactory: config.ArtifactoryMirror{URL: srv.URL + "/artifactory",
				Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}},
		Secrets: config.Secrets{MirrorPassword: "p"},
	}
	return srv, c, &seen
}

func TestResolveRejectsChannelOutsideRepo(t *testing.T) {
	srv, c, seen := recordingSrv(t, map[string][]byte{})
	defer srv.Close()
	for _, ch := range []string{"../../rondo-releases-local/x", "edge/feat", "-edge"} {
		_, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), ch, "")
		if err == nil {
			t.Errorf("channel %q accepted", ch)
			continue
		}
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("channel %q rejected for a reason other than its shape: %v", ch, err)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("a channel outside the repository was still requested over the network: %v", *seen)
	}
}

func TestResolveRejectsVersionOutsideRepo(t *testing.T) {
	srv, c, seen := recordingSrv(t, map[string][]byte{
		"/stable.json": []byte(`{"version":"../../rondo-releases-local/x"}`),
	})
	defer srv.Close()

	err := resolveErr(t, srv, c, "stable", "")
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("pointer version outside the repository: %v", err)
	}
	err = resolveErr(t, srv, c, "stable", "../../x")
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("pinned version outside the repository: %v", err)
	}
	for _, path := range *seen {
		if strings.Contains(path, "iac.tar.gz") {
			t.Errorf("an archive was requested outside generic/rondo: %s", path)
		}
	}
}

func resolveErr(t *testing.T, srv *httptest.Server, c *config.Config, channel, pinned string) error {
	t.Helper()
	_, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), channel, pinned)
	if err == nil {
		t.Fatalf("Resolve(channel=%q, version=%q) accepted", channel, pinned)
	}
	return err
}

func TestResolveChannelInvalidJSON(t *testing.T) {
	srv, c := bundleSrv(t, map[string][]byte{"/stable.json": []byte("not json")})
	defer srv.Close()
	if _, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", ""); err == nil {
		t.Fatal("invalid channel pointer accepted")
	}
}

func TestResolveChannelEmptyVersion(t *testing.T) {
	srv, c := bundleSrv(t, map[string][]byte{"/stable.json": []byte(`{"version":""}`)})
	defer srv.Close()
	if _, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", ""); err == nil {
		t.Fatal("pointer without a version accepted")
	}
}

func TestResolveChecksumMismatch(t *testing.T) {
	data, _ := makeTarGz(t, map[string]string{"a.txt": "x"})
	srv, c := bundleSrv(t, map[string][]byte{
		"/v1.0.0/iac.tar.gz.sha256": []byte("deadbeef  iac.tar.gz\n"),
		"/v1.0.0/iac.tar.gz":        data,
	})
	defer srv.Close()
	dir := t.TempDir()
	if _, err := Resolve(context.Background(), srv.Client(), c, dir, "stable", "v1.0.0"); err == nil {
		t.Fatal("invalid checksum accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "v1.0.0", ".rondo-bundle-ok")); err == nil {
		t.Error("marker written despite an invalid checksum")
	}
}

// Artifactory answers GET <file>.sha256 with the bare digest it computed, without a file name.
func TestResolveAcceptsABareDigest(t *testing.T) {
	data, sum := makeTarGz(t, map[string]string{"helmfile.yaml": "releases: []\n"})
	srv, c := bundleSrv(t, map[string][]byte{
		"/v1.0.0/iac.tar.gz.sha256": []byte(sum),
		"/v1.0.0/iac.tar.gz":        data,
	})
	defer srv.Close()
	if _, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", "v1.0.0"); err != nil {
		t.Fatalf("bare digest refused: %v", err)
	}
}

func TestResolveRefusesAChecksumWithoutDigest(t *testing.T) {
	data, _ := makeTarGz(t, map[string]string{"a.txt": "x"})
	srv, c := bundleSrv(t, map[string][]byte{
		"/v1.0.0/iac.tar.gz.sha256": []byte("<html>login</html>"),
		"/v1.0.0/iac.tar.gz":        data,
	})
	defer srv.Close()
	_, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", "v1.0.0")
	if err == nil || !strings.Contains(err.Error(), "no sha256") {
		t.Fatalf("error = %v, want a checksum without digest refused", err)
	}
}

func TestResolveSha256Missing(t *testing.T) {
	srv, c := bundleSrv(t, map[string][]byte{})
	defer srv.Close()
	if _, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", "v9"); err == nil {
		t.Fatal("missing checksum accepted")
	}
}

func TestResolveChannelHTTPError(t *testing.T) {
	srv, c := bundleSrv(t, map[string][]byte{})
	defer srv.Close()
	if _, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", ""); err == nil {
		t.Fatal("unreachable channel should fail")
	}
}

func TestResolveTarballMissing(t *testing.T) {
	srv, c := bundleSrv(t, map[string][]byte{
		"/v1.0.0/iac.tar.gz.sha256": []byte("abc  iac.tar.gz\n"),
	})
	defer srv.Close()
	if _, err := Resolve(context.Background(), srv.Client(), c, t.TempDir(), "stable", "v1.0.0"); err == nil {
		t.Fatal("missing archive should fail")
	}
}

func TestDryResolve(t *testing.T) {
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory,
		Artifactory: config.ArtifactoryMirror{URL: "https://arti.example.com/artifactory",
			Repositories: config.ArtifactoryRepositories{Generic: "rondo-generic-local"}}}}
	cache := "/cache"
	if got := DryResolve(c, cache, "", ""); got != filepath.Join(cache, "stable") {
		t.Errorf("DryResolve channel = %q", got)
	}
	if got := DryResolve(c, cache, "stable", "v1.2.3"); got != filepath.Join(cache, "v1.2.3") {
		t.Errorf("DryResolve pinned = %q", got)
	}
}

func TestRepoBaseDefaultChannel(t *testing.T) {
	c := &config.Config{Artifacts: config.Artifacts{Preset: config.PresetArtifactory,
		Artifactory: config.ArtifactoryMirror{URL: "https://x/artifactory/",
			Repositories: config.ArtifactoryRepositories{Generic: "g"}}}}
	base, ch := repoBase(c, "")
	if base != "https://x/artifactory/g/rondo" || ch != "stable" {
		t.Errorf("repoBase = %q %q", base, ch)
	}
}

func TestWriteEntryRegularFileMkdirError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sub"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &tar.Header{Name: "sub/f.txt", Mode: 0o644, Typeflag: tar.TypeReg}
	if err := writeEntry(dir, h, bytes.NewReader([]byte("data"))); err == nil {
		t.Fatal("creating a file under a file should fail")
	}
}

func TestExtractVerifiedNotGzip(t *testing.T) {
	if err := extractVerified(bytes.NewReader([]byte("not gzip")), "deadbeef", t.TempDir()); err == nil {
		t.Fatal("non-gzip stream should fail")
	}
}

func TestWriteEntryDirAndSymlinkIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := writeEntry(dir, &tar.Header{Name: "sub", Typeflag: tar.TypeDir}, nil); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "sub")); err != nil || !fi.IsDir() {
		t.Errorf("directory not created: %v", err)
	}
	if err := writeEntry(dir, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, bytes.NewReader(nil)); err != nil {
		t.Fatalf("symlink should be skipped without error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Error("symlink should not be created")
	}
}
