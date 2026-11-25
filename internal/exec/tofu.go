package exec

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/runner"
)

const (
	EnvStateBaseURL    = "RONDO_TOFU_STATE_URL"
	EnvStateS3         = kobj.EnvTofuStateS3
	EnvAllowLocalState = "RONDO_TOFU_ALLOW_LOCAL_STATE"
	EnvMirrorSecret    = "RONDO_TOFU_RC_SECRET"
	EnvMirrorSecretKey = "RONDO_TOFU_RC_KEY"
)

type tofuWorkdir struct {
	dir string
	env map[string]string
}

func (rt *Runtime) materializeTofurc(ctx context.Context, ns string) (string, error) {
	name := os.Getenv(EnvMirrorSecret)
	if name == "" {
		log.Warn("no .tofurc configured (" + EnvMirrorSecret + "), " +
			"`tofu init` will look for providers on the public registry")
		return "", nil
	}
	key := os.Getenv(EnvMirrorSecretKey)
	if key == "" {
		key = kobj.TofurcKey
	}
	var secret corev1.Secret
	if err := rt.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &secret); err != nil {
		return "", fmt.Errorf("reading Secret %s: %w", name, err)
	}
	raw, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q missing from Secret %s", key, name)
	}
	return rt.writeFile("tofurc", raw)
}

func (sc *tofuWorkdir) tofu(ctx context.Context, rt *Runtime, args ...string) error {
	return rt.Run.Run(ctx, runner.Cmd{Name: "tofu", Args: args, Dir: sc.dir, Env: sc.env})
}

func (sc *tofuWorkdir) tofuOutput(ctx context.Context, rt *Runtime, args ...string) (string, error) {
	return rt.Run.Output(ctx, runner.Cmd{Name: "tofu", Args: args, Dir: sc.dir, Env: sc.env})
}

func (sc *tofuWorkdir) initModule(ctx context.Context, rt *Runtime) error {
	if err := sc.checkMirror(ctx); err != nil {
		return err
	}
	if err := sc.tofu(ctx, rt, "init", "-input=false", "-no-color"); err != nil {
		return fmt.Errorf("tofu init (providers mirrored? .tofurc mounted?): %w", err)
	}
	return nil
}

const mirrorProbeTimeout = 30 * time.Second

func (sc *tofuWorkdir) checkMirror(ctx context.Context) error {
	path := sc.env["TF_CLI_CONFIG_FILE"]
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path written by this same process
	if err != nil {
		log.Warn("network_mirror probe NOT POSSIBLE (unreadable .tofurc), `tofu init` "+
			"will decide", "err", err)
		return nil
	}
	if registry := parseTofurcOCI(string(raw)); registry != "" {
		probeOCIMirror(ctx, registry)
		return nil
	}
	mirror, token := parseTofurcMirror(string(raw))
	if mirror == "" {
		log.Warn("network_mirror probe NOT POSSIBLE: no recognized `network_mirror` block " +
			"in the .tofurc, `tofu init` will decide")
		return nil
	}
	return probeMirror(ctx, mirror, token)
}

func probeMirror(ctx context.Context, mirror, token string) error {
	client, err := mirrorHTTPClient(mirrorProbeTimeout)
	if err != nil {
		log.Warn("network_mirror probe NOT POSSIBLE (HTTPS client), `tofu init` will decide",
			"err", err)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mirror, nil)
	if err != nil {
		log.Warn("network_mirror probe NOT POSSIBLE (invalid URL), `tofu init` will decide",
			"url", mirror, "err", err)
		return nil
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("network_mirror unreachable, probe INCONCLUSIVE, `tofu init` will decide",
			"url", mirror, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the OpenTofu network_mirror REJECTS the mounted credentials "+
			"(HTTP %d on %s): the `.tofurc` Bearer token is expired, revoked or lacks read "+
			"access to this repository. It is minted at deploy time with a finite lifetime, "+
			"and a Job started later finds it stale. Regenerate it with `rondo deploy` "+
			"(it rewrites the `rondo-tofurc` Secret). "+
			"Otherwise `tofu init` would print \"Failed to resolve provider packages\", which "+
			"does not name this cause", resp.StatusCode, mirror)
	}
	log.Info("network_mirror reachable and credentials accepted", "url", mirror,
		"http", resp.StatusCode)
	return nil
}

// parseTofurcOCI is the registry host of the oci_mirror of rc, empty when it has none.
func parseTofurcOCI(rc string) string {
	for _, line := range strings.Split(rc, "\n") {
		if field := strings.TrimSpace(line); strings.HasPrefix(field, "repository_template") {
			host, _, _ := strings.Cut(quotedValue(field), "/")
			return host
		}
	}
	return ""
}

// probeOCIMirror tells whether the registry of the OCI provider mirror answers the OCI
// distribution API. It never fails: the credentials are the mirror's own, not a token that
// expires, and an anonymous /v2/ answers 401 on most registries anyway.
func probeOCIMirror(ctx context.Context, registry string) {
	client, err := mirrorHTTPClient(mirrorProbeTimeout)
	if err != nil {
		log.Warn("oci_mirror probe NOT POSSIBLE (HTTPS client), `tofu init` will decide", "err", err)
		return
	}
	url := "https://" + registry + "/v2/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warn("oci_mirror probe NOT POSSIBLE (invalid registry), `tofu init` will decide",
			"registry", registry, "err", err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("oci_mirror registry unreachable, probe INCONCLUSIVE, `tofu init` will decide",
			"url", url, "err", err)
		return
	}
	resp.Body.Close()
	log.Info("oci_mirror registry reachable", "url", url, "http", resp.StatusCode)
}

func parseTofurcMirror(rc string) (mirror, token string) {
	for _, line := range strings.Split(rc, "\n") {
		field := strings.TrimSpace(line)
		switch {
		case mirror == "" && strings.HasPrefix(field, "url"):
			mirror = quotedValue(field)
		case token == "" && strings.HasPrefix(field, "token"):
			token = quotedValue(field)
		}
	}
	return mirror, token
}

func quotedValue(line string) string {
	first := strings.Index(line, `"`)
	if first < 0 {
		return ""
	}
	last := strings.LastIndex(line, `"`)
	if last <= first {
		return ""
	}
	return line[first+1 : last]
}
