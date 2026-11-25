package dashapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client reads a dashboard at Base, e.g. http://127.0.0.1:41234 at the end of a port-forward.
type Client struct {
	Base string
	HTTP *http.Client
}

func (c *Client) Overview(ctx context.Context) ([]Summary, error) {
	return get[[]Summary](ctx, c, PathOverview)
}

func (c *Client) Components(ctx context.Context) ([]ComponentView, error) {
	return get[[]ComponentView](ctx, c, PathComponents)
}

func (c *Client) Enrollments(ctx context.Context) ([]EnrollmentView, error) {
	return get[[]EnrollmentView](ctx, c, PathEnrollments)
}

func (c *Client) Targets(ctx context.Context) ([]TargetView, error) {
	return get[[]TargetView](ctx, c, PathTargets)
}

func (c *Client) Jobs(ctx context.Context) ([]JobView, error) {
	return get[[]JobView](ctx, c, PathJobs)
}

func (c *Client) Services(ctx context.Context) ([]ServiceView, error) {
	return get[[]ServiceView](ctx, c, PathServices)
}

func (c *Client) JobLogs(ctx context.Context, job string, tail int64) (io.ReadCloser, error) {
	return c.open(ctx, PathLogs+"?"+url.Values{"job": {job}, "tail": {strconv.FormatInt(tail, 10)}}.Encode())
}

func (c *Client) ServiceLogs(ctx context.Context, pod string, tail int64) (io.ReadCloser, error) {
	return c.open(ctx, PathLogs+"?"+url.Values{"pod": {pod}, "tail": {strconv.FormatInt(tail, 10)}}.Encode())
}

func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var out T
	body, err := c.open(ctx, path)
	if err != nil {
		return out, err
	}
	defer body.Close()
	err = json.NewDecoder(body).Decode(&out)
	if err != nil {
		err = fmt.Errorf("unreadable response from %s: %w", path, err)
	}
	return out, err
}

// open returns the body of a 200; any other status is an error carrying the dashboard's message.
func (c *Client) open(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("dashboard %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.Body, nil
}
