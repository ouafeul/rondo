package steps

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"rondo/internal/log"
)

type hostTool struct {
	name string
	pkg  string
	why  string
}

var hostToolsAlways = []hostTool{
	{"ssh-keygen", "openssh-clients", "generating the key pair for access to component machines"},
}

var hostToolsMgmtInfra = []hostTool{
	{"bash", "bash", "running scripts/k3s-setup.sh"},
	{"ssh", "openssh-clients", "forming the HA k3s cluster on the management VMs"},
	{"scp", "openssh-clients", "copying the k3s systemd unit and fetching the kubeconfig"},
	{"sshpass", "sshpass", "SSH password authentication to fresh VMs " +
		"(not in the RHEL base repositories: EPEL package, or mirrored on Artifactory)"},
}

type PreflightStage struct {
	skipMgmtInfra bool
	dryRun        bool
	lookPath      func(string) (string, error)
}

func NewPreflightStage(skipMgmtInfra, dryRun bool) *PreflightStage {
	return &PreflightStage{skipMgmtInfra: skipMgmtInfra, dryRun: dryRun, lookPath: exec.LookPath}
}

func (p *PreflightStage) AssertHostTools(_ context.Context) error {
	log.Step("host prerequisites preflight")
	needed := hostToolsAlways
	if p.skipMgmtInfra {
		log.Info("deploy downstreams: the SSH tools are not required",
			"tools", names(hostToolsMgmtInfra))
	} else {
		needed = append(append([]hostTool{}, needed...), hostToolsMgmtInfra...)
	}

	var missing []string
	for _, t := range needed {
		if _, err := p.lookPath(t.name); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", t.name, t.why))
		}
	}
	if len(missing) > 0 {
		detail := "workstation prerequisites missing, install them before running again:\n  - " +
			strings.Join(missing, "\n  - ")
		if p.dryRun {
			log.Warn("dry-run: " + detail)
			return nil
		}
		return errors.New(detail)
	}
	log.Info("workstation prerequisites present", "tools", names(needed))
	return nil
}

func names(tools []hostTool) string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.name
	}
	return strings.Join(out, ", ")
}
