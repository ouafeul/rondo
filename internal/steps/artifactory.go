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

// The JFrog Access API, under the platform root (the Artifactory URL without its /artifactory
// context): tokens are created by POST on tokensPath and revoked by DELETE on revokeSelfPath,
// authenticated by the token to revoke ("me" is the token of the request).
const (
	tokensPath     = "/access/api/v1/tokens"
	revokeSelfPath = tokensPath + "/me"
	// tokenScope gives the token the permissions of its user, those of its groups included.
	tokenScope = "applied-permissions/user"
)

type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a token given in the manifest; empty is no token.
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// ArtifactoryClient mints and revokes the tokens of the provider mirror through the JFrog Access
// API, with the mirror's account (basic auth). URL is artifacts.artifactory.url.
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
	client, err := httpx.NewForCA(a.CACertPath, apiTimeout)
	if err != nil {
		return err
	}
	a.HTTP = client
	return nil
}

// platformURL is the root of the JFrog Platform: the Artifactory URL without its /artifactory
// context, where the Access API lives.
func (a *ArtifactoryClient) platformURL() string {
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(a.URL), "/"), "/artifactory")
}

func (a *ArtifactoryClient) mintToken(ctx context.Context, expiresIn, description string) (string, error) {
	if err := a.ensureHTTP(); err != nil {
		return "", err
	}
	form := url.Values{
		"username":    {a.User},
		"scope":       {tokenScope},
		"expires_in":  {expiresIn},
		"refreshable": {"false"},
		"description": {description},
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	endpoint := a.platformURL() + tokensPath
	if err := httpx.PostForm(ctx, a.HTTP, endpoint, a.User, a.Password, form, &out); err != nil {
		return "", fmt.Errorf("JFrog access token (POST %s): %w", endpoint, err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("JFrog access token (POST %s): empty access_token", endpoint)
	}
	return out.AccessToken, nil
}

// Token is a short-lived token for the OpenTofu runs of the CLI.
func (a *ArtifactoryClient) Token(ctx context.Context) (string, error) {
	tok, err := a.mintToken(ctx, "3600", "rondo deploy: OpenTofu provider mirror (1 hour)")
	if err != nil {
		return "", a.refusal(err)
	}
	return tok, nil
}

// MintDurableToken is the token of the Jobs, kept in the rondo-tofurc Secret.
func (a *ArtifactoryClient) MintDurableToken(ctx context.Context) (string, error) {
	ttl := strings.TrimSpace(a.DurableTokenTTL)
	if ttl == "" {
		ttl = "0"
	}
	tok, err := a.mintToken(ctx, ttl, "rondo: OpenTofu provider mirror of the platform Jobs")
	if err == nil {
		return tok, nil
	}
	var se *httpx.StatusError
	if ttl == "0" && errors.As(err, &se) &&
		(se.Code == http.StatusBadRequest || se.Code == http.StatusForbidden) {
		return "", fmt.Errorf("%w\n"+
			"A NON-EXPIRING token (expires_in=0) is reserved for JFrog administrator "+
			"accounts, and %q is not one.\nThree ways out:\n"+
			"  - use an admin account for artifacts.username;\n"+
			"  - set artifacts.artifactory.mirrorTokenLifetimeSeconds (e.g. \"31536000\"); the token "+
			"WILL EXPIRE, so plan its rotation;\n"+
			"  - create a token yourself and set it in secrets.mirrorToken: RONDO then uses it "+
			"as is, and neither mints nor revokes one.", err, a.User)
	}
	return "", a.refusal(err)
}

// refusal explains a token the Access API does not mint for want of rights or of the API.
func (a *ArtifactoryClient) refusal(err error) error {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
	default:
		return err
	}
	return fmt.Errorf("%w\n"+
		"RONDO mints the token of the OpenTofu provider mirror through the JFrog Access API with "+
		"artifacts.username (%q) and secrets.mirrorPassword. That needs an internal user, without "+
		"multi-factor authentication, allowed to create tokens with basic credentials, and %s "+
		"reachable.\nOtherwise create a token yourself (JFrog Platform UI, `jf access-token-create`) "+
		"that reads the provider mirror, and set it in secrets.mirrorToken: RONDO then uses it as "+
		"is, and neither mints nor revokes one.", err, a.User, a.platformURL()+tokensPath)
}

// RevokeToken revokes a token minted by MintDurableToken: DELETE .../tokens/me authenticated by
// the token itself, which a user may do for their own tokens. A token the platform no longer
// accepts (expired, revoked) has nothing left to revoke.
func (a *ArtifactoryClient) RevokeToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := a.ensureHTTP(); err != nil {
		return err
	}
	endpoint := a.platformURL() + revokeSelfPath
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode == http.StatusUnauthorized:
		return nil
	default:
		return fmt.Errorf("revoking the token (DELETE %s): HTTP %d", endpoint, resp.StatusCode)
	}
}
