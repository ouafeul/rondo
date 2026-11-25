package dashapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, routes map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == PathLogs {
			io.WriteString(w, r.URL.RawQuery)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, "unknown "+r.URL.Path, http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL + "/"}
}

func TestClientReadsEachView(t *testing.T) {
	ctx := context.Background()
	c := serve(t, map[string]any{
		PathOverview:    []Summary{{Kind: "Target", Total: 2, Ready: 1}},
		PathComponents:  []ComponentView{{Name: "traefik"}},
		PathEnrollments: []EnrollmentView{{Name: "e", ConsecutiveFailures: 3}},
		PathTargets:     []TargetView{{Name: "management", InCluster: true}},
		PathJobs:        []JobView{{Name: "helm-traefik"}},
		PathServices:    []ServiceView{{Name: "rondo-controller", Pods: []PodView{{Name: "p"}}}},
	})
	if got, err := c.Overview(ctx); err != nil || got[0].Ready != 1 {
		t.Errorf("overview = %+v, %v", got, err)
	}
	if got, err := c.Components(ctx); err != nil || got[0].Name != "traefik" {
		t.Errorf("components = %+v, %v", got, err)
	}
	if got, err := c.Enrollments(ctx); err != nil || got[0].ConsecutiveFailures != 3 {
		t.Errorf("enrollments = %+v, %v", got, err)
	}
	if got, err := c.Targets(ctx); err != nil || !got[0].InCluster {
		t.Errorf("targets = %+v, %v", got, err)
	}
	if got, err := c.Jobs(ctx); err != nil || got[0].Name != "helm-traefik" {
		t.Errorf("jobs = %+v, %v", got, err)
	}
	if got, err := c.Services(ctx); err != nil || got[0].Pods[0].Name != "p" {
		t.Errorf("services = %+v, %v", got, err)
	}
	for want, open := range map[string]func() (io.ReadCloser, error){
		"job=helm-traefik&tail=500": func() (io.ReadCloser, error) { return c.JobLogs(ctx, "helm-traefik", 500) },
		"pod=p&tail=20":             func() (io.ReadCloser, error) { return c.ServiceLogs(ctx, "p", 20) },
	} {
		rc, err := open()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != want {
			t.Errorf("logs request %q, want %q", got, want)
		}
	}
}

func TestClientCarriesTheDashboardError(t *testing.T) {
	c := serve(t, map[string]any{PathJobs: "not a list"})
	if _, err := c.Overview(context.Background()); err == nil || !strings.Contains(err.Error(), "502") ||
		!strings.Contains(err.Error(), "unknown /api/overview") {
		t.Errorf("error = %v", err)
	}
	if _, err := c.Jobs(context.Background()); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("decoding error = %v", err)
	}
	if _, err := (&Client{Base: "http://127.0.0.1:1"}).Targets(context.Background()); err == nil {
		t.Error("closed port accepted")
	}
}
