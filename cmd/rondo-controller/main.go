package main

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"rondo/api/v1alpha1"
	"rondo/internal/controllers"
	"rondo/internal/gomem"
	"rondo/internal/jobs"
	"rondo/internal/kobj"
	"rondo/internal/log"
	"rondo/internal/version"
)

const (
	envJobTTL          = "RONDO_JOB_TTL"
	envLeaseHoldS      = "RONDO_LEASE_DURATION"
	envAllowLocalState = "RONDO_TOFU_ALLOW_LOCAL_STATE"
)

func main() {
	var probeAddr, metricsAddr, logLevel string
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address of the liveness/readiness probes")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "metrics address (0 = disabled)")
	flag.StringVar(&logLevel, "log-level", os.Getenv("RONDO_LOG_LEVEL"), "debug|info|warn|error")
	flag.Parse()

	log.Setup(logLevel)
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))
	logger := ctrl.Log.WithName("setup")

	if budget := gomem.Apply(); budget > 0 {
		logger.Info("memory budget set from the cgroup", "bytes", budget)
	}

	cfg, err := loadConfig()
	if err != nil {
		logger.Error(err, "invalid configuration")
		os.Exit(1)
	}
	logger.Info("starting", "version", version.Version, "commit", version.Commit,
		"namespace", cfg.namespace, "toolbox", cfg.jobs.Image, "holder", cfg.holder)

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			logger.Error(err, "building the scheme")
			os.Exit(1)
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{cfg.namespace: {}},
		},
		LeaderElection:          true,
		LeaderElectionID:        "rondo-controller",
		LeaderElectionNamespace: cfg.namespace,
	})
	if err != nil {
		logger.Error(err, "building the manager")
		os.Exit(1)
	}

	lock := &jobs.Lock{
		Client:    mgr.GetClient(),
		Reader:    mgr.GetAPIReader(),
		Namespace: cfg.namespace,
		Holder:    cfg.holder,
		Duration:  cfg.leaseDuration,
	}

	for _, rec := range reconcilers(reconcilerDeps{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		Jobs: cfg.jobs, Lock: lock, Reader: mgr.GetAPIReader(),
	}) {
		name := reconcilerName(rec)
		if err := rec.SetupWithManager(mgr); err != nil {
			logger.Error(err, "wiring the reconciler", "reconciler", name)
			os.Exit(1)
		}
		logger.V(1).Info("reconciler wired", "reconciler", name)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		logger.Error(err, "healthz probe")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		logger.Error(err, "readyz probe")
		os.Exit(1)
	}

	logger.Info("operator ready")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "manager stopped")
		os.Exit(1)
	}
}

type reconciler interface {
	SetupWithManager(ctrl.Manager) error
}

type reconcilerDeps struct {
	Client client.Client
	Scheme *runtime.Scheme
	Jobs   jobs.Config
	Lock   *jobs.Lock
	// Uncached (mgr.GetAPIReader()): for reads whose freshness is a safety invariant.
	Reader client.Reader
}

func reconcilers(d reconcilerDeps) []reconciler {
	return []reconciler{
		&controllers.TargetReconciler{Client: d.Client, Scheme: d.Scheme},
		&controllers.EnrollmentReconciler{
			Client: d.Client, Scheme: d.Scheme, Jobs: d.Jobs, Lock: d.Lock, Reader: d.Reader,
		},
	}
}

func reconcilerName(r reconciler) string {
	t := reflect.TypeOf(r)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

type config struct {
	namespace     string
	holder        string
	leaseDuration time.Duration
	jobs          jobs.Config
}

func loadConfig() (*config, error) {
	c := &config{
		namespace:     getenv(kobj.EnvNamespace, kobj.PlatformNamespace),
		holder:        os.Getenv(kobj.EnvPodName),
		leaseDuration: 2 * time.Minute,
	}
	if c.holder == "" {
		return nil, fmt.Errorf("%s required (downward API: metadata.name), "+
			"the lock holder's unique identity", kobj.EnvPodName)
	}
	image := os.Getenv(kobj.EnvToolboxImage)
	if image == "" {
		return nil, fmt.Errorf("%s required (toolbox image running the Jobs)", kobj.EnvToolboxImage)
	}
	if d, err := time.ParseDuration(os.Getenv(envLeaseHoldS)); err == nil && d > 0 {
		c.leaseDuration = d
	}
	ttl := 24 * time.Hour
	if d, err := time.ParseDuration(os.Getenv(envJobTTL)); err == nil && d > 0 {
		ttl = d
	}
	c.jobs = jobs.Config{
		Namespace:              c.namespace,
		Image:                  image,
		ServiceAccount:         getenv(kobj.EnvExecSA, kobj.ExecServiceAccount),
		ArtifactoryCAConfigMap: getenv(kobj.EnvCACM, kobj.ArtifactoryCACM),
		ArtifactoryCredsSecret: getenv(kobj.EnvCredsSecret, kobj.CredentialsSecret),
		TTL:                    ttl,
		TofuStateBaseURL:       os.Getenv(kobj.EnvTofuStateURL),
		TofuRCSecret:           os.Getenv(kobj.EnvTofuRCSecret),
		AllowLocalTofuState:    os.Getenv(envAllowLocalState) == "1",
		VMModuleURL:            os.Getenv(kobj.EnvVMModuleURL),
		AnsibleContentURL:      os.Getenv(kobj.EnvAnsibleContentURL),
		VMSSHSecret:            getenv(kobj.EnvVMSSHSecret, kobj.VMSSHSecret),
		ArtifactoryURL:         os.Getenv(kobj.EnvArtifactoryURL),
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
