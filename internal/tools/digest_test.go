package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// mirrorOf serves files by path; a missing path is a 404.
func mirrorOf(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// installKubectl installs kubectl alone (the others already present) from srv.
func installKubectl(t *testing.T, srv *httptest.Server, digests map[string]string) (string, error) {
	t.Helper()
	binDir := t.TempDir()
	for _, tl := range toolset(nil) {
		if tl.name != "kubectl" {
			if err := os.WriteFile(filepath.Join(binDir, tl.name), nil, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	in := &Installer{BaseURL: srv.URL, HTTP: srv.Client(), BinDir: binDir, Digests: digests}
	err := in.Install(context.Background())
	return filepath.Join(binDir, "kubectl"), err
}

const kubectlPath = "/kubectl/v1.34.13/kubectl"

func TestToolMatchingItsDigestsIsInstalled(t *testing.T) {
	bin := "#!kubectl"
	for name, tc := range map[string]struct {
		files   map[string]string
		digests map[string]string
	}{
		"pinned":            {map[string]string{kubectlPath: bin}, map[string]string{"kubectl": sum([]byte(bin))}},
		"served":            {map[string]string{kubectlPath: bin, kubectlPath + ".sha256": sum([]byte(bin)) + "  kubectl\n"}, nil},
		"pinned and served": {map[string]string{kubectlPath: bin, kubectlPath + ".sha256": strings.ToUpper(sum([]byte(bin)))}, map[string]string{"kubectl": sum([]byte(bin))}},
		"neither":           {map[string]string{kubectlPath: bin}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			dest, err := installKubectl(t, mirrorOf(t, tc.files), tc.digests)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if raw, _ := os.ReadFile(dest); string(raw) != bin {
				t.Errorf("kubectl = %q", raw)
			}
		})
	}
}

func TestToolNotMatchingItsDigestsIsRefused(t *testing.T) {
	bin, other := "#!kubectl", sum([]byte("another file"))
	for name, tc := range map[string]struct {
		files   map[string]string
		digests map[string]string
		want    string
	}{
		"pinned mismatch": {map[string]string{kubectlPath: bin}, map[string]string{"kubectl": other}, "sha256"},
		"served mismatch": {map[string]string{kubectlPath: bin, kubectlPath + ".sha256": other}, nil, "sha256"},
		"pin and served disagree": {map[string]string{kubectlPath: bin, kubectlPath + ".sha256": other},
			map[string]string{"kubectl": sum([]byte(bin))}, "dependencies.yaml pins"},
		"served garbage": {map[string]string{kubectlPath: bin, kubectlPath + ".sha256": "not a digest"}, nil, "holds no sha256"},
		"pin garbage":    {map[string]string{kubectlPath: bin}, map[string]string{"kubectl": "abc"}, "is not a sha256"},
	} {
		t.Run(name, func(t *testing.T) {
			dest, err := installKubectl(t, mirrorOf(t, tc.files), tc.digests)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install = %v, want an error mentioning %q", err, tc.want)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Error("kubectl installed although its digest does not match")
			}
		})
	}
}

func TestChecksumFileErrorFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("#!kubectl"))
	}))
	defer srv.Close()
	if _, err := installKubectl(t, srv, nil); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("Install = %v, want the checksum's HTTP 403", err)
	}
}

func TestHelmDiffIsVerified(t *testing.T) {
	srv := mirrorOf(t, map[string]string{"/" + helmDiffPath(helmDiffDefaultVersion): "not what was pinned"})
	in := &Installer{BaseURL: srv.URL, HTTP: srv.Client(), PluginDir: t.TempDir(),
		Digests: map[string]string{"helm-diff": sum([]byte("the real one"))}}
	if err := in.installHelmDiff(context.Background()); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("installHelmDiff = %v, want a sha256 mismatch", err)
	}
}
