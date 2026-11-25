// Command rondo-sandbox plays on k3d clusters what the rondo CLI plays on a site: `deploy` the
// management, `deploy downstreams` its rke2 Targets, `component install` a component, whose
// deployment RONDO then judges (docs: components/sandbox). Its site is rondo-sandbox.yaml, with
// rondo.yaml's keys. Its commands are cobra's, for help and completion; those that touch the
// infrastructure hand their arguments to the embedded script, which calls back
// `rondo-sandbox engine` for what speaks RONDO (internal/sandbox).
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"rondo/internal/log"
	"rondo/internal/sandbox"
	"rondo/internal/version"
)

//go:embed rondo-sandbox.sh
var script []byte

func main() {
	if len(os.Args) > 1 && os.Args[1] == "engine" {
		err := runEngine(os.Args[2:])
		log.Finish(err)
		if err != nil {
			log.Error("failed", "err", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(execute(os.Args[1:]))
}

// exitCode carries the script's exit status through cobra, which would otherwise print it.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func execute(args []string) int {
	root := newRoot()
	root.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := root.ExecuteContext(ctx)
	log.Finish(err)
	var code exitCode
	switch {
	case errors.As(err, &code):
		return int(code)
	case err != nil:
		fmt.Fprintln(os.Stderr, "rondo-sandbox:", err)
		return 1
	}
	return 0
}

var flagConfig string

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "rondo-sandbox",
		Short: "Plays on k3d clusters what the rondo CLI plays on a site, and tells whether RONDO judges a component deployed",
		Long: "The sandbox's site is rondo-sandbox.yaml, with rondo.yaml's keys (rondo-sandbox init writes it):\n" +
			"  rondo-sandbox deploy                     the management cluster, RONDO, its vSphere Targets\n" +
			"  rondo-sandbox deploy downstreams         one k3d cluster per downstream, rke2 Target and base layer\n" +
			"  rondo-sandbox component install <dir>    publishes, installs the component, awaits RONDO's verdict\n\n" +
			"Variables: RONDO_SRC_DIR (like --rondo-from), RONDO_SANDBOX_RONDO_REGISTRY (the registry of\n" +
			"RONDO's images), RONDO_SANDBOX_HOME (~/.rondo-sandbox), RONDO_SANDBOX_SUBNET (172.28.0.0/24),\n" +
			"RONDO_SANDBOX_REGISTRY_PORT (5100), RONDO_SANDBOX_BIND (127.0.0.1), RONDO_SANDBOX_K3S_IMAGE.",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			log.Setup("info")
		},
	}
	root.SetVersionTemplate(versionLine())
	pf := root.PersistentFlags()
	pf.StringVarP(&flagConfig, "config", "c", "rondo-sandbox.yaml", "the sandbox's site, with rondo.yaml's keys")
	pf.StringVar(&flagRondoFrom, "rondo-from", "",
		"RONDO clone: its tree, and its images built from its sources (default: RONDO_SRC_DIR)")
	pf.StringVar(&flagRondoVersion, "rondo-version", version.Version,
		"published RONDO version to pull the images of: rondo-sandbox's own by default")
	_ = root.RegisterFlagCompletionFunc("rondo-from", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	})
	_ = root.RegisterFlagCompletionFunc("config", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"yaml", "yml"}, cobra.ShellCompDirectiveFilterFileExt
	})

	var force bool
	initCmd := &cobra.Command{
		Use:   "init [component directory]",
		Short: "Writes rondo-sandbox.yaml from the component's tree: one downstream per role, its Secrets and componentValues",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return sandbox.RunInit(orDot(args), force)
		},
		ValidArgsFunction: dirArg,
	}
	initCmd.Flags().BoolVar(&force, "force", false, "rewrites targets and secrets of an existing rondo-sandbox.yaml")

	deploy := &cobra.Command{
		Use:   "deploy",
		Short: "Deploys the management cluster (k3d), RONDO, its vSphere Targets and the site's Secrets",
		Args:  cobra.NoArgs,
		RunE:  toScript,
	}
	deploy.AddCommand(&cobra.Command{
		Use:   "downstreams [target...]",
		Short: "One k3d cluster per downstream of rondo-sandbox.yaml (all by default), its rke2 Target and its base layer",
		RunE:  toScript,
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			declared, _ := sandbox.Downstreams(settings())
			return declared, cobra.ShellCompDirectiveNoFileComp
		},
	})

	destroy := &cobra.Command{
		Use:   "destroy",
		Short: "Deletes the sandbox's clusters, machines and mirror; the cache stays",
		Args:  cobra.NoArgs,
		RunE:  toScript,
	}
	destroy.Flags().Bool("purge", false, "also empties the image and chart cache")
	destroy.AddCommand(&cobra.Command{
		Use:   "downstreams [target...]",
		Short: "Removes downstreams (all by default): their Target, their base layer, their k3d cluster",
		RunE:  toScript,
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			_, deployed := sandbox.Downstreams(settings())
			return deployed, cobra.ShellCompDirectiveNoFileComp
		},
	})

	root.AddCommand(initCmd, deploy, destroy, componentCommand())
	root.AddCommand(debugCommands()...)
	root.AddCommand(dashboardCommand(), tuiCommand())
	root.AddCommand(
		&cobra.Command{Use: "status", Short: "The sandbox's Targets, then the Enrollments, URLs, machines and ports of installed components",
			Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
				return sandbox.RunStatus(cmd.Context(), settings())
			}},
		&cobra.Command{Use: "logs <enrollment>", Short: "Log of an Enrollment's last Job",
			Args: cobra.ExactArgs(1), RunE: toScript, ValidArgsFunction: enrollmentArg},
		kubeconfigCommand(),
		&cobra.Command{Use: "version", Short: "rondo-sandbox version (like --version)", Args: cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprint(cmd.OutOrStdout(), versionLine())
			}},
	)
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.ValidArgsFunction == nil && sub.Args != nil {
				sub.ValidArgsFunction = cobra.NoFileCompletions
			}
			walk(sub)
		}
	}
	walk(root)
	return root
}

// componentCommand is `rondo component`'s: install from the component's sources, uninstall.
func componentCommand() *cobra.Command {
	component := &cobra.Command{
		Use:   "component",
		Short: "Installs components on the sandbox's Targets, from their sources",
	}
	install := &cobra.Command{
		Use: "install [component directory]",
		Short: "Validates, publishes what the component's CI would publish, installs it and awaits RONDO's verdict " +
			"(current directory by default)",
		Args:              cobra.MaximumNArgs(1),
		RunE:              toScript,
		ValidArgsFunction: dirArg,
	}
	f := install.Flags()
	f.Bool("fresh", false, "uninstalls the component, and recreates its machines blank, before reinstalling it")
	f.Bool("no-build", false, "does not build the component's images (rondo-sandbox.yaml)")
	f.String("timeout", "30m", "maximum wait for the verdict")
	f.Int("max-failures", 2, "Job failures tolerated per Enrollment")
	_ = install.RegisterFlagCompletionFunc("timeout", fixed("10m", "30m", "1h"))
	_ = install.RegisterFlagCompletionFunc("max-failures", fixed("1", "2", "5"))
	uninstall := &cobra.Command{
		Use:   "uninstall <name>",
		Short: "Deletes a component's Components and Enrollments, their workloads and its machines",
		Args:  cobra.ExactArgs(1),
		RunE:  toScript,
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return sandbox.Components(settings()), cobra.ShellCompDirectiveNoFileComp
		},
	}
	var archetypes []string
	newCmd := &cobra.Command{
		Use: "new <directory>",
		Short: "Writes a hello-world component into a new directory, named after it: two charts (archetype A), " +
			"a machine (B), its rondo-sandbox.yaml",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return sandbox.RunNew(args[0], archetypes)
		},
		ValidArgsFunction: dirArg,
	}
	newCmd.Flags().StringSliceVar(&archetypes, "archetype", []string{"a", "b"}, "archetypes written: a (charts), b (machine)")
	_ = newCmd.RegisterFlagCompletionFunc("archetype", fixed("a,b", "a", "b"))
	component.AddCommand(newCmd, install, uninstall)
	return component
}

func kubeconfigCommand() *cobra.Command {
	return &cobra.Command{
		Use: "kubeconfig [target|role]",
		Short: "Exports KUBECONFIG for the cluster of this Target, or the one carrying this role (default: management): " +
			"eval \"$(rondo-sandbox kubeconfig monitoring)\"",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kc, err := sandbox.Kubeconfig(settings(), firstArg(args))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "export KUBECONFIG=%s\n", kc)
			return nil
		},
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return targetCompletions(), cobra.ShellCompDirectiveNoFileComp
		},
	}
}

// debugCommands are the ones a developer debugs a component with, between two installs.
func debugCommands() []*cobra.Command {
	check := &cobra.Command{
		Use: "check [component directory]",
		Short: "Static checks, in seconds and without a sandbox: tree, its Enrollments' Targets, site contract, " +
			"ports, charts, images",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := withRondo()
			if err != nil {
				return err
			}
			dir := orDot(args)
			config := filepath.Join(dir, "rondo-sandbox.yaml")
			if f := cmd.Flag("config"); f != nil && f.Changed {
				config = flagConfig
			}
			return sandbox.RunCheck(s, dir, config)
		},
		ValidArgsFunction: dirArg,
	}
	why := &cobra.Command{
		Use:   "why [enrollment]",
		Short: "What is blocking now: operator, nodes, Enrollments not Ready and their last Job, stopped pods",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunWhy(cmd.Context(), settings(), firstArg(args))
		},
		ValidArgsFunction: enrollmentArg,
	}

	values := &cobra.Command{
		Use:   "values <enrollment>",
		Short: "The values RONDO applies: the Component's and the Enrollment's, Target tokens resolved",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunValues(cmd.Context(), settings(), args[0], cmd.OutOrStdout())
		},
		ValidArgsFunction: enrollmentArg,
	}

	template := &cobra.Command{
		Use:   "template <enrollment>",
		Short: "The chart's manifests rendered with these values, by the toolbox's helm",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunTemplate(cmd.Context(), settings(), args[0], cmd.OutOrStdout())
		},
		ValidArgsFunction: enrollmentArg,
	}

	pods := &cobra.Command{
		Use:   "pods [enrollment]",
		Short: "The pods of an Enrollment, or of all installed components, on their cluster",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunPods(cmd.Context(), settings(), firstArg(args), cmd.OutOrStdout())
		},
		ValidArgsFunction: enrollmentArg,
	}

	var since string
	var follow bool
	tail := &cobra.Command{
		Use:   "tail <enrollment>",
		Short: "The logs of an Enrollment's pods, all containers, streamed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunTail(cmd.Context(), settings(), args[0], since, follow)
		},
		ValidArgsFunction: enrollmentArg,
	}
	tail.Flags().StringVar(&since, "since", "10m", "how far back")
	tail.Flags().BoolVarP(&follow, "follow", "f", true, "follows new logs (--follow=false: prints and returns)")
	_ = tail.RegisterFlagCompletionFunc("since", fixed("1m", "10m", "1h"))

	restart := &cobra.Command{
		Use:   "restart <enrollment>",
		Short: "Restarts an Enrollment's pods, runs its CronJobs, and removes rondo-sandbox.yaml's images from the node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunRestart(cmd.Context(), settings(), args[0])
		},
		ValidArgsFunction: enrollmentArg,
	}
	ssh := &cobra.Command{
		Use:   "ssh <machine> [-- command…]",
		Short: "A root shell on a machine of a machine component, or a command (through docker exec)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := sandbox.RunSSH(cmd.Context(), settings(), args[0], args[1:], term.IsTerminal(int(os.Stdin.Fd())))
			if err == nil && code != 0 {
				return exitCode(code)
			}
			return err
		},
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return sandbox.Machines(settings()), cobra.ShellCompDirectiveNoFileComp
		},
	}
	return []*cobra.Command{check, why, values, template, pods, tail, restart, ssh}
}

func enrollmentArg(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return sandbox.Enrollments(settings()), cobra.ShellCompDirectiveNoFileComp
}

func dirArg(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveFilterDirs
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// versionLine is what `version` and --version print.
func versionLine() string {
	return fmt.Sprintf("rondo-sandbox %s (commit %s)\n", version.Version, version.Commit)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// settings are the sandbox as the last deploy left it, with the RONDO it deployed: what
// component install, status and the debug commands read about.
func settings() sandbox.Settings {
	s := sandbox.Settings{StateDir: stateDir()}
	if raw, err := os.ReadFile(filepath.Join(s.StateDir, "rondo-dir")); err == nil {
		s.RondoDir = strings.TrimSpace(string(raw))
	}
	return s
}

// withRondo are the settings with the RONDO deploy would take: check judges a component against it.
func withRondo() (sandbox.Settings, error) {
	src, err := resolveRondo()
	if err != nil {
		return sandbox.Settings{}, err
	}
	return sandbox.Settings{StateDir: stateDir(), RondoDir: src.dir}, nil
}

// targetCompletions are the sandbox's cluster Targets and their roles, each with its cluster.
func targetCompletions() []string {
	var out []string
	for _, c := range sandbox.Clusters(settings()) {
		out = append(out, c.Target+"\tcluster "+c.Name)
		for _, r := range c.Roles {
			if r != c.Target {
				out = append(out, r+"\trole, cluster "+c.Name)
			}
		}
	}
	return out
}

func orDot(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}

func fixed(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// stateDir is the script's: RONDO_SANDBOX_HOME, else ~/.rondo-sandbox.
func stateDir() string {
	if dir := os.Getenv("RONDO_SANDBOX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rondo-sandbox")
}

func toScript(cmd *cobra.Command, args []string) error {
	var env []string
	words := scriptCommand(cmd)
	if words[0] == "deploy" {
		config, err := configPath(cmd)
		if err != nil {
			return err
		}
		env = append(env, "RONDO_SANDBOX_CONFIG="+config)
	}
	if strings.Join(words, " ") == "deploy" {
		src, err := resolveRondo()
		if err != nil {
			return err
		}
		env = append(env, src.env()...)
	}
	if code := runScript(scriptArgs(cmd, args), env...); code != 0 {
		return exitCode(code)
	}
	return nil
}

// configPath is the site file deploy reads: -c, rondo-sandbox.yaml in the current directory by
// default, which may be absent (the management alone).
func configPath(cmd *cobra.Command) (string, error) {
	config, err := filepath.Abs(flagConfig)
	if err != nil {
		return "", err
	}
	if f := cmd.Flag("config"); f != nil && f.Changed {
		if _, err := os.Stat(config); err != nil {
			return "", fmt.Errorf("-c %s: %w", flagConfig, err)
		}
	}
	return config, nil
}

// scriptCommand is the command's path under rondo-sandbox: deploy, deploy downstreams,
// component install…
func scriptCommand(cmd *cobra.Command) []string {
	return strings.Fields(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
}

// scriptArgs rebuilds the command line cobra parsed, its own flags only: the script parses it
// again, and gets the RONDO source and the site file by its environment.
func scriptArgs(cmd *cobra.Command, args []string) []string {
	out := scriptCommand(cmd)
	// NonInheritedFlags is a copy that knows the flags but not which were set.
	cmd.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		if f.Value.Type() == "bool" {
			if f.Value.String() == "true" {
				out = append(out, "--"+f.Name)
			}
			return
		}
		out = append(out, "--"+f.Name, f.Value.String())
	})
	return append(out, args...)
}

// runScript plays the embedded script with the user's arguments; it finds this binary again
// through RONDO_SANDBOX_BIN. Ctrl-C reaches the script, which is in the same process group.
func runScript(args []string, env ...string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "rondo-sandbox:", err)
		return 1
	}
	f, err := os.CreateTemp("", "rondo-sandbox-*.sh")
	if err != nil {
		fmt.Fprintln(os.Stderr, "rondo-sandbox:", err)
		return 1
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(script); err != nil {
		fmt.Fprintln(os.Stderr, "rondo-sandbox:", err)
		return 1
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "rondo-sandbox:", err)
		return 1
	}
	signal.Ignore(os.Interrupt, syscall.SIGTERM)
	cmd := exec.Command("bash", append([]string{f.Name()}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(append(os.Environ(), "RONDO_SANDBOX_BIN="+self), env...)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "rondo-sandbox: bash:", err)
		return 1
	}
	return 0
}

func runEngine(args []string) error {
	var s sandbox.Settings
	root := &cobra.Command{
		Use:           "rondo-sandbox engine",
		Short:         "Sandbox engine, called by the embedded script",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			log.Setup("info")
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&s.StateDir, "state", "", "sandbox state directory")
	pf.StringVar(&s.RondoDir, "rondo-dir", "", "RONDO clone (CRDs, RONDO tree, apps)")
	_ = root.MarkPersistentFlagRequired("state")
	_ = root.MarkPersistentFlagRequired("rondo-dir")

	var config, ipPrefix string
	var downstreams bool
	site := &cobra.Command{
		Use:   "site [downstream...]",
		Short: "Reads rondo-sandbox.yaml for deploy (the management cluster) or deploy downstreams, and writes the clusters to create",
		RunE: func(_ *cobra.Command, args []string) error {
			return sandbox.RunSite(s, config, ipPrefix, downstreams, args)
		},
	}
	site.Flags().StringVar(&config, "config", "", "rondo-sandbox.yaml")
	site.Flags().StringVar(&ipPrefix, "ip-prefix", "", "first three octets of the sandbox subnet")
	site.Flags().BoolVar(&downstreams, "downstreams", false, "the named downstreams (all by default), rather than the management cluster")
	_ = site.MarkFlagRequired("config")
	_ = site.MarkFlagRequired("ip-prefix")

	var registry, rondoVersion string
	publishRondo := &cobra.Command{
		Use:   "publish-rondo",
		Short: "Publishes RONDO's charts, its machine module and its playbook to the sandbox's mirror",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sandbox.RunPublishRondo(cmd.Context(), s, registry, rondoVersion)
		},
	}
	publishRondo.Flags().StringVar(&registry, "registry", "", "zot, as seen from the workstation (127.0.0.1:<port>)")
	publishRondo.Flags().StringVar(&rondoVersion, "rondo-version", "", "RONDO version under which to publish its machine module and its playbook")
	_ = publishRondo.MarkFlagRequired("registry")

	var opts sandbox.DeployOptions
	deploy := &cobra.Command{
		Use:   "deploy",
		Short: "Installs RONDO on the management cluster, its Targets and the site's Secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sandbox.RunDeploy(cmd.Context(), s, opts)
		},
	}
	deploy.Flags().StringVar(&opts.RondoVersion, "rondo-version", "", "tag of the rondo-controller and rondo-toolbox images")
	deploy.Flags().DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "maximum wait for the base layer")
	_ = deploy.MarkFlagRequired("rondo-version")

	var downOpts sandbox.DeployOptions
	down := &cobra.Command{
		Use:   "downstreams [downstream...]",
		Short: "Sets up the rke2 Target and base layer of the named downstreams (all deployed ones by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunDownstreams(cmd.Context(), s, args, downOpts)
		},
	}
	down.Flags().StringVar(&downOpts.RondoVersion, "rondo-version", "", "deployed RONDO version")
	down.Flags().DurationVar(&downOpts.Timeout, "timeout", 30*time.Minute, "maximum wait for the base layer")
	_ = down.MarkFlagRequired("rondo-version")

	destroyDown := &cobra.Command{
		Use:   "destroy-downstreams [downstream...]",
		Short: "Removes the base layer and Target of the named downstreams, and writes the clusters to delete",
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunDestroyDownstreams(cmd.Context(), s, args)
		},
	}

	plan := &cobra.Command{
		Use:   "plan <component directory>",
		Short: "Validates the component's tree against the site, and writes machines, ports and proxied repositories",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return sandbox.RunPlan(s, args[0], ipPrefix)
		},
	}
	plan.Flags().StringVar(&ipPrefix, "ip-prefix", "", "first three octets of the sandbox subnet")
	_ = plan.MarkFlagRequired("ip-prefix")

	var noBuild bool
	publish := &cobra.Command{
		Use:   "publish <component>",
		Short: "Publishes the component's charts, generic files, images and machine files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunPublish(cmd.Context(), s, args[0], registry, noBuild)
		},
	}
	publish.Flags().StringVar(&registry, "registry", "", "zot, as seen from the workstation (127.0.0.1:<port>)")
	publish.Flags().BoolVar(&noBuild, "no-build", false, "does not build the component's images")
	_ = publish.MarkFlagRequired("registry")

	var installOpts sandbox.DeployOptions
	install := &cobra.Command{
		Use:   "install <component>",
		Short: "Applies the component's tree, and awaits its verdict",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunInstall(cmd.Context(), s, args[0], installOpts)
		},
	}
	install.Flags().BoolVar(&installOpts.Fresh, "fresh", false, "uninstalls the component before reinstalling it")
	install.Flags().DurationVar(&installOpts.Timeout, "timeout", 30*time.Minute, "maximum wait for the verdict")
	install.Flags().Int32Var(&installOpts.MaxFailures, "max-failures", 2, "Job failures tolerated per Enrollment")

	uninstall := &cobra.Command{
		Use:   "uninstall <component>",
		Short: "Uninstalls the component and forgets its plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunUninstall(cmd.Context(), s, args[0])
		},
	}

	root.AddCommand(site, publishRondo, deploy, down, destroyDown, plan, publish, install, uninstall)
	root.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return root.ExecuteContext(ctx)
}
