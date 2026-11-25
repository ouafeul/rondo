package bundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"rondo/internal/config"
	"rondo/internal/httpx"
	"rondo/internal/log"
)

type pointer struct {
	Version string `json:"version"`
}

var segmentPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func checkSegment(kind, value string) error {
	if !segmentPattern.MatchString(value) {
		return fmt.Errorf("invalid %s %q: want a single path segment "+
			"(`stable`, `edge-<branch>`, `vX.Y.Z`)", kind, value)
	}
	return nil
}

func repoBase(c *config.Config, channel string) (base, effChannel string) {
	if channel == "" {
		channel = "stable"
	}
	return strings.TrimRight(c.Artifactory.URL, "/") + "/" + c.Artifactory.GenericRepository + "/rondo", channel
}

func DryResolve(c *config.Config, cacheDir, channel, pinned string) string {
	base, channel := repoBase(c, channel)
	if pinned == "" {
		log.Info("DRY-RUN resolve channel", "channel", channel, "url", base+"/"+channel+".json")
		return filepath.Join(cacheDir, channel)
	}
	log.Info("DRY-RUN download IaC bundle", "version", pinned, "url", base+"/"+pinned+"/iac.tar.gz")
	return filepath.Join(cacheDir, pinned)
}

func Resolve(ctx context.Context, client httpx.Doer, c *config.Config, cacheDir, channel, pinned string) (string, error) {
	base, channel := repoBase(c, channel)
	user, pass := c.Artifactory.Username, c.Secrets.ArtifactoryPassword

	ver := pinned
	if ver == "" {
		if err := checkSegment("channel", channel); err != nil {
			return "", err
		}
		var err error
		if ver, err = resolveChannel(ctx, client, user, pass, base, channel); err != nil {
			return "", fmt.Errorf("resolve channel %s: %w", channel, err)
		}
	}
	if err := checkSegment("version", ver); err != nil {
		return "", err
	}

	dir := filepath.Join(cacheDir, ver)
	marker := filepath.Join(dir, ".rondo-bundle-ok")
	if _, err := os.Stat(marker); err == nil {
		log.Info("IaC bundle already present, reused", "version", ver, "dir", dir)
		return dir, nil
	}

	log.Step("downloading IaC bundle", "version", ver)
	wantSum, err := httpx.GetText(ctx, client, user, pass, base+"/"+ver+"/iac.tar.gz.sha256")
	if err != nil {
		return "", fmt.Errorf("checksum iac.tar.gz.sha256: %w", err)
	}
	body, err := httpx.Get(ctx, client, user, pass, base+"/"+ver+"/iac.tar.gz")
	if err != nil {
		return "", err
	}
	defer body.Close()

	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := extractVerified(body, firstField(wantSum), dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(marker, []byte(ver+"\n"), 0o644); err != nil {
		return "", err
	}
	log.Info("IaC bundle extracted", "version", ver, "dir", dir)
	return dir, nil
}

func resolveChannel(ctx context.Context, client httpx.Doer, user, pass, base, channel string) (string, error) {
	txt, err := httpx.GetText(ctx, client, user, pass, base+"/"+channel+".json")
	if err != nil {
		return "", err
	}
	var p pointer
	if err := json.Unmarshal([]byte(txt), &p); err != nil {
		return "", fmt.Errorf("invalid channel pointer: %w", err)
	}
	if p.Version == "" {
		return "", fmt.Errorf("channel pointer has no version field")
	}
	return p.Version, nil
}

func extractVerified(r io.Reader, wantHex, dir string) error {
	h := sha256.New()
	tee := io.TeeReader(r, h)
	gz, err := gzip.NewReader(tee)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := writeEntry(dir, hdr, tr); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantHex) {
		return fmt.Errorf("SHA-256 checksum mismatch: want %s, got %s", wantHex, got)
	}
	return nil
}

func writeEntry(dir string, h *tar.Header, r io.Reader) error {
	target := filepath.Join(dir, h.Name)
	clean := filepath.Clean(dir)
	if target != clean && !strings.HasPrefix(target, clean+string(os.PathSeparator)) {
		return fmt.Errorf("tar entry outside the target directory: %s", h.Name)
	}
	switch h.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, r) //nolint:gosec // size bounded by the trusted (CI) archive, checksum verified
		return err
	default:
		return nil
	}
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
