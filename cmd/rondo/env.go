package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"rondo/internal/kubeconfig"
	"rondo/internal/log"
	"rondo/internal/paths"
)

// defaultEnv names the unprefixed environment: $RONDO_HOME/rondo.yaml, or ./rondo.yaml in the repo.
const defaultEnv = "default"

func cmdEnv() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Lists and selects environments (to create one: rondo init --env <name>)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Lists the environments, * marks the selected one",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cur := envLabel(currentEnv())
			for _, e := range listEnvs() {
				mark := " "
				if e == cur {
					mark = "*"
				}
				fmt.Println(mark, e)
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "select <name>",
		Short: "Selects the environment of the following commands and its kubectl context",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return selectEnv(args[0])
		},
	})
	return cmd
}

func currentEnv() string {
	raw, err := os.ReadFile(paths.CurrentEnvFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func envLabel(env string) string {
	if env == "" {
		return defaultEnv
	}
	return env
}

func listEnvs() []string {
	envs := []string{defaultEnv}
	entries, _ := os.ReadDir(filepath.Join(paths.Home(), "envs"))
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(paths.EnvDir(e.Name()), "rondo.yaml")); err == nil {
			envs = append(envs, e.Name())
		}
	}
	return envs
}

func selectEnv(name string) error {
	if name == defaultEnv {
		name = ""
	}
	if name != "" {
		if _, err := os.Stat(filepath.Join(paths.EnvDir(name), "rondo.yaml")); err != nil {
			return fmt.Errorf("unknown environment %q, create it with: rondo init --env %s", name, name)
		}
	}
	if err := os.MkdirAll(paths.Home(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(paths.CurrentEnvFile(), []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	kctx := kubeconfig.ContextName(name)
	found, err := kubeconfig.Select(kctx)
	if err != nil {
		return err
	}
	if !found {
		kctx = "none (created by the next deploy)"
	}
	log.Step("environment selected", "env", envLabel(name), "kubectl", kctx)
	return nil
}
