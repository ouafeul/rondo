package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/internal/httpx"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/runner"
)

const (
	EnvTargetRef           = "RONDO_TARGET_REF"
	EnvTargetKind          = "RONDO_TARGET_KIND"
	EnvNamespace           = kobj.EnvNamespace
	EnvArtifactoryUser     = kobj.CredentialsUserKey
	EnvArtifactoryPassword = kobj.CredentialsPasswordKey
	EnvCAFile              = "SSL_CERT_FILE"
)

const workDir = "/work"

const fieldManager = "rondo-exec"

type Runtime struct {
	Client    client.Client
	Run       runner.CommandRunner
	Namespace string
	Ref       types.NamespacedName
	Kind      string
	WorkDir   string
}

func RefFromEnv() (types.NamespacedName, string, string, error) {
	raw := os.Getenv(EnvTargetRef)
	kind := os.Getenv(EnvTargetKind)
	ns := os.Getenv(EnvNamespace)
	if raw == "" || kind == "" {
		return types.NamespacedName{}, "", "", fmt.Errorf(
			"%s and %s required (injected by internal/jobs)", EnvTargetRef, EnvTargetKind)
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return types.NamespacedName{}, "", "", fmt.Errorf(
			"%s=%q: want <namespace>/<name>", EnvTargetRef, raw)
	}
	if ns == "" {
		ns = parts[0]
	}
	return types.NamespacedName{Namespace: parts[0], Name: parts[1]}, kind, ns, nil
}

func (rt *Runtime) dir() string {
	if rt.WorkDir != "" {
		return rt.WorkDir
	}
	return workDir
}

func (rt *Runtime) writeFile(name string, content []byte) (string, error) {
	path := filepath.Join(rt.dir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

func artifactoryCreds() (user, password string, err error) {
	user = os.Getenv(EnvArtifactoryUser)
	password = os.Getenv(EnvArtifactoryPassword)
	if user == "" || password == "" {
		return "", "", fmt.Errorf("%s and %s required (credentials Secret mounted via envFrom)",
			EnvArtifactoryUser, EnvArtifactoryPassword)
	}
	return user, password, nil
}

const maxArchiveBytes = 256 << 20

func downloadArchive(ctx context.Context, url, dest string) error {
	user, password, err := artifactoryCreds()
	if err != nil {
		return err
	}
	client, err := archiveHTTPClient()
	if err != nil {
		return err
	}
	body, err := httpx.Get(ctx, client, user, password, url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer body.Close()
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(body, maxArchiveBytes))
	if err != nil {
		return err
	}
	log.Info("archive downloaded", "bytes", n)
	return nil
}

func archiveHTTPClient() (httpx.Doer, error) {
	const timeout = 10 * time.Minute
	return artifactoryHTTPClient(timeout)
}

func artifactoryHTTPClient(timeout time.Duration) (httpx.Doer, error) {
	if ca := os.Getenv(EnvCAFile); ca != "" {
		return httpx.NewCAPinned(ca, timeout)
	}
	return httpx.NewDefault(timeout), nil
}

func registryHost(ociURL string) (string, error) {
	rest := strings.TrimPrefix(ociURL, "oci://")
	if rest == ociURL {
		return "", fmt.Errorf("chart URL %q: want an oci:// prefix", ociURL)
	}
	host, _, _ := strings.Cut(rest, "/")
	if host == "" {
		return "", fmt.Errorf("chart URL %q has no authority", ociURL)
	}
	return host, nil
}

func logStep(msg string, args ...any) {
	log.Step(msg, args...)
}

type Verb func(ctx context.Context, rt *Runtime) error

var Verbs = map[string]Verb{
	"helm-apply":     HelmApply,
	"helm-uninstall": HelmUninstall,
}

func Dispatch(ctx context.Context, name string, rt *Runtime) error {
	verb, ok := Verbs[name]
	if !ok {
		known := make([]string, 0, len(Verbs))
		for k := range Verbs {
			known = append(known, k)
		}
		return fmt.Errorf("unknown verb %q (known: %s)", name, strings.Join(known, ", "))
	}
	return verb(ctx, rt)
}
