package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewCAPinned(caPath string, timeout time.Duration) (*http.Client, error) {
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA %s: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid CA %s", caPath)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}, nil
}

// NewForCA trusts the CA bundle at caPath, or the system trust store when caPath is empty.
func NewForCA(caPath string, timeout time.Duration) (*http.Client, error) {
	if caPath == "" {
		return NewDefault(timeout), nil
	}
	return NewCAPinned(caPath, timeout)
}

func NewDefault(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
	}
}

func NewInsecure(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // in-cluster service, self-signed cert
	}
}

func Get(ctx context.Context, d Doer, user, password, urlStr string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d on %s", resp.StatusCode, urlStr)
	}
	return resp.Body, nil
}

func GetText(ctx context.Context, d Doer, user, password, urlStr string) (string, error) {
	body, err := Get(ctx, d, user, password, urlStr)
	if err != nil {
		return "", err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func PostJSON(ctx context.Context, d Doer, urlStr, bearer string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return doJSON(d, req, out)
}

func PostForm(ctx context.Context, d Doer, urlStr, user, password string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(user, password)
	return doJSON(d, req, out)
}

type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Code)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body)
}

const maxErrBody = 2 << 10

func errBody(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, maxErrBody))
	return strings.Join(strings.Fields(string(raw)), " ")
}

func doJSON(d Doer, req *http.Request, out any) error {
	resp, err := d.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{Code: resp.StatusCode, Body: errBody(resp.Body)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
