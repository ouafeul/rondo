package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"rondo/internal/kubeconfig"
	"rondo/internal/paths"
	"rondo/internal/tui"
	"rondo/internal/version"
)

var flagKubeContext string

func cmdTUI() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Terminal interface: the management cluster's dashboard and the CLI's commands",
		Long: "Reads the management cluster through its dashboard (Overview, Components, Enrollments,\n" +
			"Targets, Jobs & logs, Services, Installations), and runs the CLI's commands (deploy, destroy,\n" +
			"component, env, init), showing their steps and answering their confirmations.\n\n" +
			"The dashboard is reached through `kubectl port-forward`, with the kubectl context\n" +
			"of the environment (rondo-<env>) that `rondo deploy` installs in ~/.kube/config.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			config := ""
			if cmd.Flags().Changed("config") {
				config = flagConfig
			}
			initial := flagEnv
			return tui.Run(cmd.Context(), tui.Options{
				Executable:   exe,
				Version:      version.Version,
				Env:          flagEnv,
				Config:       config,
				Forward:      forwardedFlags(),
				DryRun:       flagDryRun,
				SkipDownload: flagSkipDownload,
				Connect: func(env string) (tui.Source, string, error) {
					kctx := kubeconfig.ContextName(env)
					if env == initial && flagKubeContext != "" {
						kctx = flagKubeContext
					}
					kubectl, err := kubectlPath()
					if err != nil {
						return nil, kctx, err
					}
					src, err := tui.ConnectRemote(kubectl, kctx)
					if err != nil {
						return nil, kctx, err
					}
					return src, kctx, nil
				},
				Envs:       listEnvs,
				ConfigPath: envConfigPath,
			})
		},
	}
	cmd.Flags().StringVar(&flagKubeContext, "kube-context", "",
		"kubectl context of the management cluster (empty => the environment's)")
	return cmd
}

// forwardedFlags are the global flags every command the TUI launches keeps; never --yes, the TUI
// asks.
func forwardedFlags() []string {
	var out []string
	if flagLogLevel != "info" {
		out = append(out, "--log-level", flagLogLevel)
	}
	if flagIacVersion != "" {
		out = append(out, "--iac-version", flagIacVersion)
	}
	if flagIacChannel != "" {
		out = append(out, "--iac-channel", flagIacChannel)
	}
	return out
}

// kubectlPath is the kubectl rondo downloaded ($RONDO_HOME/bin, or .local/bin in the repository),
// else the PATH's.
func kubectlPath() (string, error) {
	for _, p := range []string{filepath.Join(paths.BinDir(), "kubectl"), filepath.Join(".local", "bin", "kubectl")} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return filepath.Abs(p)
		}
	}
	p, err := exec.LookPath("kubectl")
	if err != nil {
		return "", fmt.Errorf("kubectl not found ($RONDO_HOME/bin, .local/bin, PATH): rondo deploy downloads it")
	}
	return p, nil
}
