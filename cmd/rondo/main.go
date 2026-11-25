package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"rondo/internal/log"
	"rondo/internal/paths"
)

var (
	flagConfig       string
	flagEnv          string
	flagLogLevel     string
	flagDebug        bool
	flagDryRun       bool
	flagSkipDownload bool
	flagYes          bool
	flagIacVersion   string
	flagIacChannel   string
	flagForce        bool
)

func main() {
	err := run()
	log.Finish(err)
	if err != nil {
		log.Error("failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	root := &cobra.Command{
		Use:           "rondo",
		Short:         "RONDO platform orchestrator (k3s management cluster + RKE2 downstreams)",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			if flagDebug && flagLogLevel == "info" {
				flagLogLevel = "debug"
			}
			log.Setup(flagLogLevel)
			resolveConfigPath()
		},
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&flagConfig, "config", "c", "rondo.yaml", "path of the rondo.yaml manifest")
	pf.StringVar(&flagEnv, "env", "", "targeted environment ($RONDO_HOME/envs/<name>); empty => the one from `rondo env select`")
	pf.StringVar(&flagLogLevel, "log-level", "info", "log level: trace|debug|info|warn|error")
	pf.BoolVar(&flagDebug, "debug", false, "debug mode (same as --log-level debug)")
	pf.BoolVar(&flagDryRun, "dry-run", false, "shows the plan without changing anything; implies --yes")
	pf.BoolVar(&flagSkipDownload, "skip-download", false, "reuses the tools already downloaded")
	pf.BoolVar(&flagYes, "yes", false, "asks for no confirmation")
	pf.StringVar(&flagIacVersion, "iac-version", "", "pins the IaC bundle version (standalone); empty => the followed channel")
	pf.StringVar(&flagIacChannel, "iac-channel", "", "IaC bundle channel (standalone): stable (default) or edge-<branch>; takes precedence over artifacts.releaseChannel")

	root.AddCommand(cmdVersion(), cmdInit(), cmdDeploy(), cmdDestroy(), cmdComponent(), cmdEnv(), cmdTUI())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return root.ExecuteContext(ctx)
}

// resolveConfigPath picks the environment (--env, else `rondo env select` unless --config is set), then its rondo.yaml.
func resolveConfigPath() {
	explicit := flagConfig != "rondo.yaml"
	switch {
	case flagEnv == defaultEnv:
		flagEnv = ""
	case flagEnv == "" && !explicit:
		flagEnv = currentEnv()
	}
	if !explicit {
		flagConfig = envConfigPath(flagEnv)
	}
}

// envConfigPath is env's rondo.yaml; the default environment's is ./rondo.yaml, else $RONDO_HOME's.
func envConfigPath(env string) string {
	if env != "" {
		return filepath.Join(paths.EnvDir(env), "rondo.yaml")
	}
	if _, err := os.Stat("rondo.yaml"); err == nil {
		return "rondo.yaml"
	}
	alt := filepath.Join(paths.Home(), "rondo.yaml")
	if _, err := os.Stat(alt); err == nil {
		return alt
	}
	return "rondo.yaml"
}
