package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"rondo/internal/log"
)

type Cmd struct {
	Name      string
	Args      []string
	Dir       string
	Env       map[string]string
	Stdin     string
	WaitDelay time.Duration
}

func (c Cmd) String() string { return c.Name + " " + strings.Join(c.Args, " ") }

type CommandRunner interface {
	Run(ctx context.Context, c Cmd) error
	// Output is never short-circuited by dry-run: read-only calls only.
	Output(ctx context.Context, c Cmd) (string, error)
}

type Exec struct {
	baseEnv map[string]string
}

func NewExec(baseEnv map[string]string) *Exec { return &Exec{baseEnv: baseEnv} }

func (e *Exec) envSlice(c Cmd) []string {
	merged := make(map[string]string, len(e.baseEnv)+len(c.Env))
	for k, v := range e.baseEnv {
		merged[k] = v
	}
	for k, v := range c.Env {
		merged[k] = v
	}
	out := os.Environ()
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}

func (e *Exec) command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = e.envSlice(c)
	cmd.WaitDelay = c.WaitDelay
	return cmd
}

type lineWriter struct{ buf []byte }

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		log.StreamLine(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		log.StreamLine(string(w.buf))
		w.buf = nil
	}
}

func (e *Exec) Run(ctx context.Context, c Cmd) error {
	if c.Stdin != "" {
		log.Debug("exec(pipe)", "cmd", c.String())
	} else {
		log.Debug("exec", "cmd", c.String(), "dir", c.Dir)
	}
	cmd := e.command(ctx, c)
	lw := &lineWriter{}
	cmd.Stdout = lw
	cmd.Stderr = lw
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	} else {
		cmd.Stdin = os.Stdin
	}
	err := cmd.Run()
	lw.flush()
	if err != nil {
		if c.Stdin != "" {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		return fmt.Errorf("%s: %w", c.String(), err)
	}
	return nil
}

func (e *Exec) Output(ctx context.Context, c Cmd) (string, error) {
	cmd := e.command(ctx, c)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", c.String(), err, errb.String())
	}
	return out.String(), nil
}

const DryRunReadTimeout = 5 * time.Second

type DryRun struct {
	Real        CommandRunner
	ReadTimeout time.Duration
}

func (d DryRun) readTimeout() time.Duration {
	if d.ReadTimeout > 0 {
		return d.ReadTimeout
	}
	return DryRunReadTimeout
}

func (d DryRun) Run(_ context.Context, c Cmd) error {
	if c.Stdin != "" {
		log.Info("DRY-RUN " + c.String() + " (stdin masked)")
		return nil
	}
	log.Info("DRY-RUN " + c.String())
	return nil
}

func (d DryRun) Output(ctx context.Context, c Cmd) (string, error) {
	timeout := d.readTimeout()
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	c.WaitDelay = timeout
	out, err := d.Real.Output(ctx, c)
	if err == nil {
		return out, nil
	}
	if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("%s: read abandoned after %s, a preview does not wait for "+
			"a remote service (cluster destroyed, unreachable or overloaded?): %w",
			c.String(), timeout, err)
	}
	return out, err
}
