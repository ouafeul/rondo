package exec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tofurcFor(t *testing.T, mirrorURL, token string) *tofuWorkdir {
	t.Helper()
	t.Setenv(EnvCAFile, "")
	path := filepath.Join(t.TempDir(), "tofurc")
	rc := fmt.Sprintf(
		"provider_installation {\n  network_mirror {\n    url = %q\n  }\n}\ncredentials %q {\n  token = %q\n}\n",
		mirrorURL, "arti.local", token)
	if err := os.WriteFile(path, []byte(rc), 0o600); err != nil {
		t.Fatalf("writing .tofurc: %v", err)
	}
	return &tofuWorkdir{env: map[string]string{"TF_CLI_CONFIG_FILE": path}}
}

func TestMirrorProbeNamesCredentialRejection(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("HTTP %d", code), func(t *testing.T) {
			var seen string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Get("Authorization")
				w.WriteHeader(code)
			}))
			defer srv.Close()

			sc := tofurcFor(t, srv.URL+"/api/terraform/rondo-tofu-local/providers/", "tok-expired")
			err := sc.checkMirror(context.Background())
			if err == nil {
				t.Fatal("no error on a credential rejection: `tofu init` would print " +
					"\"Failed to resolve provider packages\", which does not name the cause")
			}
			for _, want := range []string{fmt.Sprintf("HTTP %d", code), "expired", "rondo deploy", "rondo-tofurc"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message does not mention %q: %v", want, err)
				}
			}
			if seen != "Bearer tok-expired" {
				t.Errorf("Authorization header = %q: the probe did not present the .tofurc "+
					"token, so it does not test what `tofu init` will test", seen)
			}
		})
	}
}

func TestMirrorProbeLetsEverythingElseThrough(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("HTTP %d", code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()
			sc := tofurcFor(t, srv.URL+"/providers/", "tok")
			if err := sc.checkMirror(context.Background()); err != nil {
				t.Fatalf("HTTP %d rejected by the probe: it blocks an apply that `tofu init` "+
					"could have completed, %v", code, err)
			}
		})
	}
}

func TestMirrorProbeLetsUnreachableRegistryThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/providers/"
	srv.Close()

	sc := tofurcFor(t, url, "tok")
	if err := sc.checkMirror(context.Background()); err != nil {
		t.Fatalf("unreachable registry treated as a credential rejection: %v", err)
	}
}

func TestMirrorProbeAbstainsWhenInconclusive(t *testing.T) {
	ctx := context.Background()

	t.Run("no .tofurc configured", func(t *testing.T) {
		sc := &tofuWorkdir{env: map[string]string{}}
		if err := sc.checkMirror(ctx); err != nil {
			t.Fatalf("error without .tofurc: a module with no external provider does not need one, %v", err)
		}
	})

	t.Run("unreadable .tofurc", func(t *testing.T) {
		sc := &tofuWorkdir{env: map[string]string{
			"TF_CLI_CONFIG_FILE": filepath.Join(t.TempDir(), "missing"),
		}}
		if err := sc.checkMirror(ctx); err != nil {
			t.Fatalf("error on an unreadable .tofurc: the Job must let `tofu init` decide, %v", err)
		}
	})

	t.Run("no recognized network_mirror", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tofurc")
		if err := os.WriteFile(path, []byte("# nothing to probe\n"), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		sc := &tofuWorkdir{env: map[string]string{"TF_CLI_CONFIG_FILE": path}}
		if err := sc.checkMirror(ctx); err != nil {
			t.Fatalf("error on a .tofurc without network_mirror: %v", err)
		}
	})
}

func TestParseTofurcMirrorReadsTheFormActuallyWritten(t *testing.T) {
	const want = "https://arti.example/api/terraform/rondo-tofu-local/providers/"
	rc := fmt.Sprintf(
		"provider_installation {\n  network_mirror {\n    url = %q\n  }\n}\ncredentials %q {\n  token = %q\n}\n",
		want, "arti.example", "AKCp8-secret")
	mirror, token := parseTofurcMirror(rc)
	if mirror != want {
		t.Errorf("url = %q, want %q", mirror, want)
	}
	if token != "AKCp8-secret" {
		t.Errorf("token = %q, want %q", token, "AKCp8-secret")
	}

	mirror, token = parseTofurcMirror("provider_installation {\n  network_mirror {\n    url = \"https://a/b/\"\n  }\n}\n")
	if mirror != "https://a/b/" || token != "" {
		t.Errorf("anonymous mirror misread: url=%q token=%q", mirror, token)
	}
}
