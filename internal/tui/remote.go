package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"rondo/internal/dashapi"
	"rondo/internal/kobj"
)

// Source is what the TUI reads of the management cluster: the dashboard's views, plus the trees
// `rondo component install` recorded.
type Source interface {
	Overview(ctx context.Context) ([]dashapi.Summary, error)
	Components(ctx context.Context) ([]dashapi.ComponentView, error)
	Enrollments(ctx context.Context) ([]dashapi.EnrollmentView, error)
	Targets(ctx context.Context) ([]dashapi.TargetView, error)
	Jobs(ctx context.Context) ([]dashapi.JobView, error)
	Services(ctx context.Context) ([]dashapi.ServiceView, error)
	Trees(ctx context.Context) ([]Tree, error)
	JobLogs(ctx context.Context, job string, tail int64) (io.ReadCloser, error)
	ServiceLogs(ctx context.Context, pod string, tail int64) (io.ReadCloser, error)
	Close() error
}

// Tree is what one `rondo component install` applied, read back from its record ConfigMap.
type Tree struct {
	Name    string
	Version string
	Objects []string
}

const portForwardTimeout = 20 * time.Second

// Remote reads the management cluster with kubectl, as the CLI does: the dashboard through a
// `kubectl port-forward`, the records with `kubectl get`. The rondo binary links no Kubernetes
// client (scripts/ci/check-rondo-deps.sh).
type Remote struct {
	kubectl   string
	context   string
	namespace string

	mu     sync.Mutex
	pf     *exec.Cmd
	exited chan struct{}
	api    *dashapi.Client
}

// ConnectRemote checks that kubectl knows kctx; the port-forward starts on the first read.
func ConnectRemote(kubectl, kctx string) (*Remote, error) {
	out, err := exec.Command(kubectl, "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		return nil, kubectlError("kubectl config get-contexts", err)
	}
	for _, c := range strings.Fields(string(out)) {
		if c == kctx {
			return &Remote{kubectl: kubectl, context: kctx, namespace: kobj.PlatformNamespace}, nil
		}
	}
	return nil, fmt.Errorf("kubectl context %s not found (rondo deploy adds it to ~/.kube/config)", kctx)
}

func (r *Remote) args(args ...string) []string {
	return append([]string{"--context", r.context, "-n", r.namespace}, args...)
}

var forwarding = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)

// client is the dashboard at the end of the port-forward, started again when it has died.
func (r *Remote) client(ctx context.Context) (*dashapi.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.api != nil {
		select {
		case <-r.exited:
			r.api = nil
		default:
			return r.api, nil
		}
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(r.kubectl, r.args("port-forward", "svc/"+kobj.DashboardName, fmt.Sprintf(":%d", kobj.DashboardPort))...)
	var stderr lockedBuffer
	cmd.Stdout, cmd.Stderr = pw, &stderr
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("kubectl port-forward: %w", err)
	}
	pw.Close()
	ports := make(chan string, 1)
	// kubectl prints a line per connection: read to the end, or it blocks on a full pipe.
	go func() {
		defer pr.Close()
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if m := forwarding.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case ports <- m[1]:
				default:
				}
			}
		}
	}()
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	select {
	case port := <-ports:
		r.pf, r.exited = cmd, exited
		r.api = &dashapi.Client{Base: "http://127.0.0.1:" + port}
		return r.api, nil
	case <-exited:
		return nil, fmt.Errorf("kubectl port-forward svc/%s: %s", kobj.DashboardName, strings.TrimSpace(stderr.String()))
	case <-ctx.Done():
	case <-time.After(portForwardTimeout):
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("kubectl port-forward svc/%s: no port opened", kobj.DashboardName)
}

func (r *Remote) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pf == nil {
		return nil
	}
	err := r.pf.Process.Kill()
	<-r.exited
	r.pf, r.api = nil, nil
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func call[T any](ctx context.Context, r *Remote, read func(*dashapi.Client, context.Context) (T, error)) (T, error) {
	c, err := r.client(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	return read(c, ctx)
}

func (r *Remote) Overview(ctx context.Context) ([]dashapi.Summary, error) {
	return call(ctx, r, (*dashapi.Client).Overview)
}

func (r *Remote) Components(ctx context.Context) ([]dashapi.ComponentView, error) {
	return call(ctx, r, (*dashapi.Client).Components)
}

func (r *Remote) Enrollments(ctx context.Context) ([]dashapi.EnrollmentView, error) {
	return call(ctx, r, (*dashapi.Client).Enrollments)
}

func (r *Remote) Targets(ctx context.Context) ([]dashapi.TargetView, error) {
	return call(ctx, r, (*dashapi.Client).Targets)
}

func (r *Remote) Jobs(ctx context.Context) ([]dashapi.JobView, error) {
	return call(ctx, r, (*dashapi.Client).Jobs)
}

func (r *Remote) Services(ctx context.Context) ([]dashapi.ServiceView, error) {
	return call(ctx, r, (*dashapi.Client).Services)
}

func (r *Remote) JobLogs(ctx context.Context, job string, tail int64) (io.ReadCloser, error) {
	return call(ctx, r, func(c *dashapi.Client, ctx context.Context) (io.ReadCloser, error) {
		return c.JobLogs(ctx, job, tail)
	})
}

func (r *Remote) ServiceLogs(ctx context.Context, pod string, tail int64) (io.ReadCloser, error) {
	return call(ctx, r, func(c *dashapi.Client, ctx context.Context) (io.ReadCloser, error) {
		return c.ServiceLogs(ctx, pod, tail)
	})
}

// Trees reads the records as `rondo component uninstall` does, with kubectl.
func (r *Remote) Trees(ctx context.Context) ([]Tree, error) {
	out, err := exec.CommandContext(ctx, r.kubectl, r.args("get", "configmaps", "-o", "json")...).Output()
	if err != nil {
		return nil, kubectlError("kubectl get configmaps", err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("kubectl get configmaps: unreadable output: %w", err)
	}
	prefix := kobj.RecordName("")
	var trees []Tree
	for _, cm := range list.Items {
		objects, ok := cm.Data["objects"]
		if !ok || !strings.HasPrefix(cm.Metadata.Name, prefix) {
			continue
		}
		t := Tree{Name: strings.TrimPrefix(cm.Metadata.Name, prefix), Version: cm.Data["version"]}
		for _, o := range strings.Split(objects, "\n") {
			if o = strings.TrimSpace(o); o != "" {
				t.Objects = append(t.Objects, o)
			}
		}
		trees = append(trees, t)
	}
	sort.Slice(trees, func(i, j int) bool { return trees[i].Name < trees[j].Name })
	return trees, nil
}

func kubectlError(what string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%s: %s", what, strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// lockedBuffer is kubectl's stderr, written by exec's copier while the model may read it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
