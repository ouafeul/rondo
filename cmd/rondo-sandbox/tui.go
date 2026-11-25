package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"rondo/internal/sandbox"
	"rondo/internal/tui"
	"rondo/internal/version"
)

func tuiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "The RONDO terminal interface on the sandbox, read-only: Components, Enrollments, Targets, Jobs & logs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd.Context())
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
}

// runTUI is `rondo tui` on the sandbox's management, read-only: its commands (deploy, destroy,
// component, init) act on a site's rondo.yaml, which the sandbox is not.
func runTUI(ctx context.Context) error {
	kc, err := sandbox.Kubeconfig(settings(), "")
	if err != nil {
		return err
	}
	if _, err := os.Stat(kc); err != nil {
		return fmt.Errorf("no sandbox cluster (%s): rondo-sandbox deploy", kc)
	}
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found in PATH")
	}
	// Every kubectl the TUI runs reads the sandbox's kubeconfig, never the workstation's.
	if err := os.Setenv("KUBECONFIG", kc); err != nil {
		return err
	}
	out, err := exec.Command(kubectl, "config", "current-context").Output() //nolint:gosec // fixed arguments
	if err != nil {
		return fmt.Errorf("kubeconfig context %s: %w", kc, err)
	}
	kctx := strings.TrimSpace(string(out))
	return tui.Run(ctx, tui.Options{
		Version:  version.Version,
		Env:      "sandbox",
		ReadOnly: true,
		Connect: func(string) (tui.Source, string, error) {
			src, err := tui.ConnectRemote(kubectl, kctx)
			if err != nil {
				return nil, kctx, err
			}
			return src, kctx, nil
		},
		Envs: func() []string { return []string{"sandbox"} },
	})
}
