package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"rondo/internal/httpx"
)

type RancherClient struct {
	BaseURL string
	HTTP    httpx.Doer
}

func NewRancherClient(hostname string) *RancherClient {
	return &RancherClient{
		BaseURL: "https://" + hostname,
		HTTP:    httpx.NewInsecure(apiTimeout),
	}
}

func (r *RancherClient) ValidToken(ctx context.Context, access, secret string) bool {
	if access == "" || secret == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+"/v3/users?me=true", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+access+":"+secret)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (r *RancherClient) Login(ctx context.Context, user, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": password})
	var out struct {
		Token string `json:"token"`
	}
	if err := httpx.PostJSON(ctx, r.HTTP, r.BaseURL+"/v3-public/localProviders/local?action=login", "", body, &out); err != nil {
		return "", fmt.Errorf("Rancher login: %w", err)
	}
	return out.Token, nil
}

func (r *RancherClient) CreateToken(ctx context.Context, session, description string) (access, secret string, err error) {
	body, _ := json.Marshal(map[string]any{"type": "token", "description": description, "ttl": 0})
	var out struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := httpx.PostJSON(ctx, r.HTTP, r.BaseURL+"/v3/token", session, body, &out); err != nil {
		return "", "", fmt.Errorf("creating the Rancher API token: %w", err)
	}
	secret = out.Token
	if i := strings.IndexByte(secret, ':'); i >= 0 {
		secret = secret[i+1:]
	}
	if out.Name == "" || secret == "" {
		return "", "", fmt.Errorf("invalid Rancher API token")
	}
	return out.Name, secret, nil
}
