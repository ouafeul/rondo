package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"rondo/internal/httpx"
	"rondo/internal/log"
)

// maxDigestBytes bounds a <file>.sha256: a digest and a file name.
const maxDigestBytes = 4 << 10

// verifiedFile is a download checked against its digest, removed on Close.
type verifiedFile struct{ *os.File }

func (f verifiedFile) Close() error {
	err := f.File.Close()
	os.Remove(f.Name())
	return err
}

// download fetches url into a temporary file and checks its sha256 against the digest
// dependencies.yaml pins for the tool and the one the mirror serves at <url>.sha256: either
// mismatch fails, and so do a pin and a served digest that disagree. With neither, the tool is
// installed unverified, with a warning.
func (in *Installer) download(ctx context.Context, name, url string) (io.ReadCloser, error) {
	want, err := in.expectedDigest(ctx, name, url)
	if err != nil {
		return nil, err
	}
	body, err := httpx.Get(ctx, in.HTTP, in.User, in.Password, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	f, err := os.CreateTemp("", "rondo-"+name+"-*")
	if err != nil {
		return nil, err
	}
	out := verifiedFile{f}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		out.Close()
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	switch {
	case want == "":
		log.Warn("installed WITHOUT checksum verification: no sha256 in dependencies.yaml "+
			"nor <file>.sha256 on the mirror", "tool", name, "sha256", got)
	case got != want:
		out.Close()
		return nil, fmt.Errorf("%s: sha256 %s, want %s: the mirror serves another file than the "+
			"one expected, refused", url, got, want)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		out.Close()
		return nil, err
	}
	return out, nil
}

// expectedDigest is the sha256 url must have: dependencies.yaml's, checked against the
// mirror's <url>.sha256 when it serves one; empty when neither knows it.
func (in *Installer) expectedDigest(ctx context.Context, name, url string) (string, error) {
	pinned := in.Digests[name]
	if pinned != "" && !isSHA256(pinned) {
		return "", fmt.Errorf("dependencies.yaml: sha256 of %s (%q) is not a sha256", name, pinned)
	}
	served, err := in.servedDigest(ctx, url+".sha256")
	if err != nil {
		return "", err
	}
	if pinned != "" && served != "" && pinned != served {
		return "", fmt.Errorf("%s.sha256 is %s but dependencies.yaml pins %s: refused", url, served, pinned)
	}
	if pinned != "" {
		return pinned, nil
	}
	return served, nil
}

// servedDigest reads a sha256sum-style file ("<hex>" or "<hex>  <name>"); empty when the mirror
// has none (404). Any other answer, or a file that holds no digest, fails closed.
func (in *Installer) servedDigest(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if in.User != "" {
		req.SetBasicAuth(in.User, in.Password)
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("checksum %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", nil
	case http.StatusOK:
	default:
		return "", fmt.Errorf("checksum %s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDigestBytes))
	if err != nil {
		return "", fmt.Errorf("checksum %s: %w", url, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 || !isSHA256(strings.ToLower(fields[0])) {
		return "", fmt.Errorf("checksum %s holds no sha256", url)
	}
	return strings.ToLower(fields[0]), nil
}

func isSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
