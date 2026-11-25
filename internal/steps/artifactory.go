package steps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rondo/internal/httpx"
)

const apiTimeout = 60 * time.Second

type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type ArtifactoryClient struct {
	URL, User, Password string
	CACertPath          string
	HTTP                httpx.Doer
	DurableTokenTTL     string
}

func (a *ArtifactoryClient) ensureHTTP() error {
	if a.HTTP != nil {
		return nil
	}
	client, err := httpx.NewCAPinned(a.CACertPath, apiTimeout)
	if err != nil {
		return err
	}
	a.HTTP = client
	return nil
}

func (a *ArtifactoryClient) mintToken(ctx context.Context, expiresIn string) (string, error) {
	if err := a.ensureHTTP(); err != nil {
		return "", err
	}
	form := url.Values{"username": {a.User}, "scope": {"member-of-groups:*"}, "expires_in": {expiresIn}}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := httpx.PostForm(ctx, a.HTTP, strings.TrimRight(a.URL, "/")+"/api/security/token",
		a.User, a.Password, form, &out); err != nil {
		return "", fmt.Errorf("Artifactory token: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty Artifactory token")
	}
	return out.AccessToken, nil
}

func (a *ArtifactoryClient) Token(ctx context.Context) (string, error) {
	return a.mintToken(ctx, "3600")
}

func (a *ArtifactoryClient) MintDurableToken(ctx context.Context) (string, error) {
	ttl := strings.TrimSpace(a.DurableTokenTTL)
	if ttl == "" {
		ttl = "0"
	}
	tok, err := a.mintToken(ctx, ttl)
	if err == nil {
		return tok, nil
	}
	var se *httpx.StatusError
	if ttl == "0" && errors.As(err, &se) && se.Code == http.StatusBadRequest {
		return "", fmt.Errorf("%w\n"+
			"A NON-EXPIRING token (expires_in=0) is reserved for Artifactory administrator "+
			"accounts, and %q is not one.\nThree ways out:\n"+
			"  - use an admin account for artifactory.user;\n"+
			"  - set artifactory.durableTokenTtl (seconds, e.g. \"31536000\"); the token "+
			"WILL EXPIRE, so plan its rotation;\n"+
			"  - pin a token by hand in artifactory.mirrorToken.", err, a.User)
	}
	return "", err
}

func (a *ArtifactoryClient) RevokeToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := a.ensureHTTP(); err != nil {
		return err
	}
	form := url.Values{"token": {token}}
	return httpx.PostFormRaw(ctx, a.HTTP, strings.TrimRight(a.URL, "/")+"/api/security/token/revoke",
		a.User, a.Password, form)
}
