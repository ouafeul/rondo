package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
	"rondo/internal/dashboard"
	"rondo/internal/exec"
	"rondo/internal/gomem"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/probe"
	"rondo/internal/runner"
	"rondo/internal/version"
)

func main() {
	log.Setup(os.Getenv("RONDO_LOG_LEVEL"))

	if budget := gomem.Apply(); budget > 0 {
		log.Info("memory budget set from the cgroup", "bytes", budget)
	}

	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "exec":
		if len(os.Args) < 3 {
			usage()
		}
		if err := runExec(os.Args[2]); err != nil {
			log.Error("verb failed", "verb", os.Args[2], "err", err)
			os.Exit(1)
		}
	case "probe":
		if err := runProbe(); err != nil {
			log.Error("rondo-probe stopped on error", "err", err)
			os.Exit(1)
		}
	case "dashboard":
		if err := runDashboard(); err != nil {
			log.Error("rondo-dashboard stopped on error", "err", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("rondo-toolbox %s (commit %s)\n", version.Version, version.Commit)
	default:
		usage()
	}
}

func usage() {
	verbs := make([]string, 0, len(exec.Verbs))
	for v := range exec.Verbs {
		verbs = append(verbs, v)
	}
	fmt.Fprintf(os.Stderr, "usage: rondo-toolbox exec <%s>\n", strings.Join(verbs, "|"))
	fmt.Fprintln(os.Stderr, "       rondo-toolbox probe|dashboard")
	fmt.Fprintln(os.Stderr, "       rondo-toolbox version")
	os.Exit(2)
}

func runExec(verb string) error {
	ref, kind, namespace, err := exec.RefFromEnv()
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("in-cluster configuration (does the pod have a ServiceAccount?): %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("client to the local apiserver: %w", err)
	}

	rt := &exec.Runtime{
		Client:    c,
		Run:       runner.NewExec(nil),
		Namespace: namespace,
		Ref:       ref,
		Kind:      kind,
	}
	return exec.Dispatch(context.Background(), verb, rt)
}

func runProbe() error {
	ns := os.Getenv(exec.EnvNamespace)
	if ns == "" {
		ns = kobj.PlatformNamespace
	}
	interval := 30 * time.Second
	if d, err := time.ParseDuration(os.Getenv("RONDO_PROBE_INTERVAL")); err == nil && d > 0 {
		interval = d
	}

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("configuration in-cluster: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("client to the local apiserver: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return (&probe.Prober{Client: c, Namespace: ns, Interval: interval}).Run(ctx)
}

func runDashboard() error {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("configuration in-cluster: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("client to the local apiserver: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("clientset (pod logs): %w", err)
	}
	ns := getenv(exec.EnvNamespace, kobj.PlatformNamespace)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return (&dashboard.Server{
		Client:    c,
		Logs:      &dashboard.PodLogs{Clientset: cs, Namespace: ns},
		Namespace: ns,
		Addr:      getenv("RONDO_DASHBOARD_ADDR", ":8080"),
	}).Run(ctx)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
