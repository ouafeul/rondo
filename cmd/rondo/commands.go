package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"rondo"
	"strings"

	"github.com/spf13/cobra"

	"rondo/internal/config"
	"rondo/internal/log"
	"rondo/internal/paths"
	"rondo/internal/steps"
	"rondo/internal/tui"
	"rondo/internal/version"
)

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Prints the version and the commit",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Printf("rondo %s (commit %s)\n", version.Version, version.Commit)
			return nil
		},
	}
}

func cmdInit() *cobra.Command {
	var preset string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Creates ~/.rondo and an rondo.yaml to fill in",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			template, err := rondo.ExampleConfigFor(preset)
			if err != nil {
				return err
			}
			home := paths.Home()
			for _, d := range []string{filepath.Join(home, "bin"), filepath.Join(home, "certs"), filepath.Join(home, "cache")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(paths.StateDir(flagEnv), 0o700); err != nil {
				return err
			}
			dest := filepath.Join(paths.EnvDir(flagEnv), "rondo.yaml")
			if _, err := os.Stat(dest); err == nil && !flagForce {
				return fmt.Errorf("%s already exists (--force to overwrite it)", dest)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			content := strings.Replace(string(template),
				"environmentName: prod", "environmentName: "+flagEnv, 1)
			if flagEnv == "" {
				content = strings.Replace(content, "environmentName: \n", "", 1)
			}
			if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
				return err
			}
			log.Step("rondo.yaml written, to fill in", "path", dest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagForce, "force", false, "overwrites an existing rondo.yaml")
	cmd.Flags().StringVar(&preset, "preset", config.PresetGeneric,
		"artifact mirror: generic (any OCI registry and HTTP file server) or artifactory (JFrog Artifactory)")
	return cmd
}

func cmdDeploy() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploys the management cluster, its vSphere Targets and the RONDO components",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := loadDeps(cmd.Context())
			if err != nil {
				return err
			}
			return steps.Execute(cmd.Context(), d.deployPlan())
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "downstreams [target...]",
		Short: "Provisions the rke2 targets (all by default) and installs the base layer on them",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := loadDeps(cmd.Context())
			if err != nil {
				return err
			}
			targets, err := selectDownstreams(d.cfg, args)
			if err != nil {
				return err
			}
			return steps.Execute(cmd.Context(), d.downstreamsPlan(targets))
		},
	})
	return cmd
}

func cmdDestroy() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Destroys the management cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := confirm("Confirm destroying the management cluster"); err != nil {
				return err
			}
			d, err := loadDeps(cmd.Context())
			if err != nil {
				return err
			}
			return steps.Execute(cmd.Context(), d.destroyPlan())
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "downstreams [target...]",
		Short: "Destroys rke2 targets (all by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := loadDeps(cmd.Context())
			if err != nil {
				return err
			}
			targets, err := selectDownstreams(d.cfg, args)
			if err != nil {
				return err
			}
			names := make([]string, len(targets))
			for i, t := range targets {
				names[i] = t.Name
			}
			if err := confirm("Confirm destroying " + strings.Join(names, ", ")); err != nil {
				return err
			}
			return steps.Execute(cmd.Context(), d.destroyDownstreamsPlan(targets))
		},
	})
	return cmd
}

func loadDeps(ctx context.Context) (*deps, error) {
	c, err := config.Load(flagConfig)
	if err != nil {
		return nil, err
	}
	return newDeps(ctx, c)
}

func confirm(question string) error {
	if flagYes || flagDryRun {
		return nil
	}
	fmt.Fprint(os.Stderr, question+tui.PromptSuffix)
	var ans string
	_, _ = fmt.Scanln(&ans)
	if ans != "y" && ans != "Y" {
		return fmt.Errorf("cancelled")
	}
	return nil
}
