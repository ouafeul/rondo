package steps

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"rondo/internal/config"
	"rondo/internal/kubeconfig"
	"rondo/internal/log"
	"rondo/internal/runner"
)

type MgmtStage struct {
	cfg    *config.Config
	ws     Workspace
	run    runner.CommandRunner
	dryRun bool

	// UseContext switches kubectl to this environment, the one `rondo env select` picked.
	UseContext bool

	probe    func(ip string) bool
	interval time.Duration
	attempts int
}

func NewMgmtStage(cfg *config.Config, ws Workspace, run runner.CommandRunner, dryRun bool) *MgmtStage {
	m := &MgmtStage{cfg: cfg, ws: ws, run: run, dryRun: dryRun, interval: 10 * time.Second, attempts: 60}
	m.probe = func(ip string) bool { return cloudInitDone(ip, m.sshConfig()) }
	return m
}

func (m *MgmtStage) sshConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            m.cfg.Targets.Management.NodeSSHUsername,
		Auth:            []ssh.AuthMethod{ssh.Password(m.cfg.Targets.Management.NodeSSHPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // fresh VMs, no known_hosts
		Timeout:         5 * time.Second,
	}
}

func (m *MgmtStage) WaitCloudInit(ctx context.Context) error {
	log.Step("waiting for cloud-init to finish (management VMs)")
	if m.dryRun {
		log.Info("DRY-RUN wait for cloud-init on the VMs")
		return nil
	}
	for _, vm := range m.cfg.Targets.Management.ServerNodes {
		log.Info("waiting for cloud-init", "vm", vm.IP)
		done := false
		for attempt := 1; attempt <= m.attempts; attempt++ {
			if m.probe(vm.IP) {
				log.Info("cloud-init done", "vm", vm.IP)
				done = true
				break
			}
			log.Debug("cloud-init running", "vm", vm.IP, "attempt", attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.interval):
			}
		}
		if !done {
			return fmt.Errorf("cloud-init timeout on %s after %v", vm.IP, time.Duration(m.attempts)*m.interval)
		}
	}
	log.Info("cloud-init done on every VM")
	return nil
}

func cloudInitDone(ip string, cfg *ssh.ClientConfig) bool {
	client, err := ssh.Dial("tcp", net.JoinHostPort(ip, "22"), cfg)
	if err != nil {
		return false
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(
		"test -f /var/lib/cloud/instance/cloud-init-complete && echo DONE || echo WAITING")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "DONE")
}

func (m *MgmtStage) SetupK3s(ctx context.Context) error {
	log.Step("forming the HA k3s cluster")
	env := map[string]string{
		"SSH_USER":        m.cfg.Targets.Management.NodeSSHUsername,
		"SSH_PASSWORD":    m.cfg.Targets.Management.NodeSSHPassword,
		"K3S_TOKEN":       m.cfg.Secrets.K3sClusterToken,
		"KUBECONFIG_PATH": m.ws.Kubeconfig(),
	}
	for i, vm := range m.cfg.Targets.Management.ServerNodes {
		env[fmt.Sprintf("VM%d_IP", i+1)] = vm.IP
	}
	return m.run.Run(ctx, runner.Cmd{
		Name: "bash", Args: []string{filepath.Join(m.ws.ProjectDir, "scripts", "k3s-setup.sh")},
		Dir: m.ws.ProjectDir, Env: env,
	})
}

func (m *MgmtStage) InstallKubeconfig(_ context.Context) error {
	name := kubeconfig.ContextName(m.cfg.EnvironmentName)
	if m.dryRun {
		log.Info("DRY-RUN install kubeconfig -> ~/.kube/config", "context", name)
		return nil
	}
	raw, err := os.ReadFile(m.ws.Kubeconfig())
	if err != nil {
		return fmt.Errorf("kubeconfig.yaml not found: %w", err)
	}
	return kubeconfig.Install(raw, name, m.UseContext)
}
