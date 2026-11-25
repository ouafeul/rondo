package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"rondo/internal/bundle"
	"rondo/internal/config"
	"rondo/internal/httpx"
	"rondo/internal/log"
	"rondo/internal/paths"
	"rondo/internal/platform"
)

var (
	flagComponentVersion string
	flagComponentChannel string
	flagComponentFrom    string

	flagLintCRDs string
	flagLintDeps string
)

func cmdComponent() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "component",
		Short: "Installs components on the Targets",
	}

	install := &cobra.Command{
		Use:   "install <name>",
		Short: "Applies the Components and Enrollments a component publishes",
		Long: "Downloads the tree a component publishes\n" +
			"  <artifactory>/<generic>/<name>/platform/platform-<version>.tar.gz\n" +
			"checks its `.sha256` checksum, then applies its Components and Enrollments.\n\n" +
			"Without --version, the version is the one of the channel pointer `latest.json`.\n" +
			"--from installs from an already extracted local directory, without network.\n\n" +
			"What the installed version delivered and this one does not is deleted,\n" +
			"with its workload on the Target.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := config.Load(flagConfig)
			if err != nil {
				return err
			}
			return runComponentInstall(cmd.Context(), c, args[0])
		},
	}
	install.Flags().StringVar(&flagComponentVersion, "version", "",
		"pins a published version (empty => the channel version)")
	install.Flags().StringVar(&flagComponentChannel, "channel", bundle.ComponentChannel,
		"channel followed when --version is empty")
	install.Flags().StringVar(&flagComponentFrom, "from", "",
		"local directory holding the already extracted tree (components/, enrollments/)")

	uninstall := &cobra.Command{
		Use:   "uninstall <name>",
		Short: "Deletes a component's Components and Enrollments, and their workloads",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := config.Load(flagConfig)
			if err != nil {
				return err
			}
			if err := confirm("Uninstall " + args[0] + " from all its Targets"); err != nil {
				return err
			}
			return runComponentUninstall(cmd.Context(), c, args[0])
		},
	}

	cmd.AddCommand(install, uninstall, componentLint())
	return cmd
}

func componentLint() *cobra.Command {
	lint := &cobra.Command{
		Use:   "lint [directory]",
		Short: "Validates the tree a component publishes (default: the current directory)",
		Long: "Validates components/ and enrollments/ against the CRD schema: unknown field,\n" +
			"dangling reference, cyclic dependsOn. Exits 1 on an error.",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			findings, err := platform.ValidateDir(root, flagLintCRDs, flagLintDeps)
			if err != nil {
				return err
			}
			return reportFindings(root, findings)
		},
	}
	lint.Flags().StringVar(&flagLintCRDs, "crds", defaultCRDDir(),
		"directory of the generated CRDs (schema source)")
	lint.Flags().StringVar(&flagLintDeps, "deps", "",
		"the COMPONENT's dependencies manifest, to check that each pinned chart "+
			"is mirrored (empty => check skipped)")
	return lint
}

func defaultCRDDir() string {
	if dir := os.Getenv("RONDO_CRDS_DIR"); dir != "" {
		return dir
	}
	return "manifests/crds"
}

func runComponentInstall(ctx context.Context, c *config.Config, name string) error {
	d, err := newDeps(ctx, c)
	if err != nil {
		return err
	}
	src, version := flagComponentFrom, "local"
	if src == "" {
		client, err := httpx.NewCAPinned(d.ws.CACert, downloadTimeout)
		if err != nil {
			return err
		}
		src, version, err = bundle.ResolveComponent(ctx, client, c, paths.CacheDir(), name,
			flagComponentChannel, flagComponentVersion)
		if err != nil {
			return err
		}
	}
	return d.platform.ApplyTree(ctx, name, src, version)
}

func runComponentUninstall(ctx context.Context, c *config.Config, name string) error {
	d, err := newDeps(ctx, c)
	if err != nil {
		return err
	}
	return d.platform.UninstallTree(ctx, name)
}

func reportFindings(root string, findings []platform.Finding) error {
	var errors, warnings int
	for _, f := range findings {
		if f.Level == platform.LevelError {
			errors++
			log.Error(f.Path + ": " + f.Message)
			continue
		}
		warnings++
		log.Warn(f.Path + ": " + f.Message)
	}
	if errors > 0 {
		return fmt.Errorf("%d error(s), %d warning(s) in %s",
			errors, warnings, root)
	}
	log.Info("tree valid", "path", root, "warnings", warnings)
	return nil
}
