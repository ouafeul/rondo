package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rondo/internal/httpx"
	"rondo/internal/log"
)

type tool struct {
	name  string
	path  string
	kind  string
	inner string
}

func def(env map[string]string, key, fallback string) string {
	if v := env[key]; v != "" {
		return v
	}
	return fallback
}

func toolset(env map[string]string) []tool {
	tofu := def(env, "TOFU_VERSION", "1.11.14")
	helm := def(env, "HELM_VERSION", "v3.22.1")
	helmfile := def(env, "HELMFILE_VERSION", "0.171.0")
	kubectl := def(env, "KUBECTL_VERSION", "v1.34.13")
	k9s := def(env, "K9S_VERSION", "v0.40.5")
	yq := def(env, "YQ_VERSION", "v4.44.6")
	return []tool{
		{"tofu", fmt.Sprintf("opentofu/%s/tofu_%s_linux_amd64.zip", tofu, tofu), "zip", "tofu"},
		{"helm", fmt.Sprintf("helm/%s/helm-%s-linux-amd64.tar.gz", helm, helm), "targz", "helm"},
		{"helmfile", fmt.Sprintf("helmfile/%s/helmfile_%s_linux_amd64.tar.gz", helmfile, helmfile), "targz", "helmfile"},
		{"kubectl", fmt.Sprintf("kubectl/%s/kubectl", kubectl), "raw", ""},
		{"k9s", fmt.Sprintf("k9s/%s/k9s_Linux_amd64.tar.gz", k9s), "targz", "k9s"},
		{"yq", fmt.Sprintf("yq/%s/yq_linux_amd64", yq), "raw", ""},
	}
}

type Installer struct {
	BaseURL        string
	User, Password string
	HTTP           httpx.Doer
	BinDir         string
	PluginDir      string
	Versions       map[string]string
	// Digests are the sha256 dependencies.yaml pins, by tool name (versions.ToolDigests).
	Digests map[string]string
}

// Last helm-diff whose plugin.yaml uses command/hooks; later ones need Helm 3.18.
const helmDiffDefaultVersion = "v3.9.14"

func helmDiffPath(version string) string {
	return fmt.Sprintf("helm-diff/%s/helm-diff-linux-amd64.tgz", version)
}

func (in *Installer) Install(ctx context.Context) error {
	if err := in.each(func(t tool, dest string) error {
		log.Info("downloading", "tool", t.name)
		if err := in.fetchAndInstall(ctx, t, dest); err != nil {
			return err
		}
		log.Info("installed", "tool", t.name)
		return nil
	}); err != nil {
		return err
	}
	return in.installHelmDiff(ctx)
}

func (in *Installer) Describe(_ context.Context) error {
	if err := in.each(func(t tool, _ string) error {
		log.Info("DRY-RUN download", "tool", t.name, "url", in.BaseURL+"/"+t.path)
		return nil
	}); err != nil {
		return err
	}
	if in.PluginDir != "" {
		v := def(in.Versions, "HELM_DIFF_VERSION", helmDiffDefaultVersion)
		log.Info("DRY-RUN plugin helm-diff", "url", in.BaseURL+"/"+helmDiffPath(v), "dir", in.PluginDir)
	}
	return nil
}

func (in *Installer) installHelmDiff(ctx context.Context) error {
	if in.PluginDir == "" {
		return nil
	}
	dest := filepath.Join(in.PluginDir, "diff")
	if _, err := os.Stat(dest); err == nil {
		log.Info("helm-diff plugin already present, skip", "dir", dest)
		return nil
	}
	if err := os.MkdirAll(in.PluginDir, 0o755); err != nil {
		return err
	}
	v := def(in.Versions, "HELM_DIFF_VERSION", helmDiffDefaultVersion)
	log.Info("downloading", "tool", "helm-diff", "version", v)
	body, err := in.download(ctx, "helm-diff", in.BaseURL+"/"+helmDiffPath(v))
	if err != nil {
		return fmt.Errorf("plugin helm-diff: %w", err)
	}
	defer body.Close()
	if err := extractTarGzTree(body, in.PluginDir); err != nil {
		return fmt.Errorf("extract helm-diff plugin: %w", err)
	}
	log.Info("installed", "tool", "helm-diff", "dir", dest)
	return nil
}

func (in *Installer) each(fn func(t tool, dest string) error) error {
	log.Step("downloading tools", "dir", in.BinDir)
	if err := os.MkdirAll(in.BinDir, 0o755); err != nil {
		return err
	}
	for _, t := range toolset(in.Versions) {
		dest := filepath.Join(in.BinDir, t.name)
		if _, err := os.Stat(dest); err == nil {
			log.Info("tool already present, skip", "tool", t.name)
			continue
		}
		if err := fn(t, dest); err != nil {
			return fmt.Errorf("tool %s: %w", t.name, err)
		}
	}
	return nil
}

func (in *Installer) fetchAndInstall(ctx context.Context, t tool, dest string) error {
	body, err := in.download(ctx, t.name, in.BaseURL+"/"+t.path)
	if err != nil {
		return err
	}
	defer body.Close()
	switch t.kind {
	case "raw":
		return writeExec(dest, body)
	case "zip":
		return extractZip(body, t.binaryName(), dest)
	case "targz":
		return extractTarGz(body, t.binaryName(), dest)
	}
	return fmt.Errorf("unknown type %q", t.kind)
}

func (t tool) binaryName() string {
	if t.inner != "" {
		return t.inner
	}
	return t.name
}

func writeExec(dest string, r io.Reader) error {
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func extractZip(r io.Reader, binary, dest string) error {
	tmp, err := os.CreateTemp("", "rondo-zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	size, err := io.Copy(tmp, r)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == binary {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return writeExec(dest, rc)
		}
	}
	return fmt.Errorf("binary %q missing from the zip", binary)
}

func extractTarGz(r io.Reader, binary, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == binary {
			return writeExec(dest, tr)
		}
	}
	return fmt.Errorf("binary %q missing from the tar.gz", binary)
}

func extractTarGzTree(r io.Reader, destRoot string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanRoot := filepath.Clean(destRoot)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(cleanRoot, h.Name)
		if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) {
			return fmt.Errorf("entry outside the target directory: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if strings.Contains(filepath.ToSlash(h.Name), "/bin/") {
				mode = 0o755
			}
			if err := writeFileMode(target, tr, mode); err != nil {
				return err
			}
		}
	}
}

func writeFileMode(dest string, r io.Reader, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
