package jobs

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const lockNamespace = "default"

var testEnvCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	testEnvCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

func leaseEnv(t *testing.T) client.Client {
	t.Helper()
	if testEnvCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS not set (setup-envtest)")
	}
	cfg := testEnvCfg
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fixedClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fixedClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func newLock(c client.Client, holder string, clk *fixedClock) *Lock {
	return &Lock{
		Client:    c,
		Namespace: lockNamespace,
		Holder:    holder,
		Duration:  30 * time.Second,
		Now:       clk.now,
	}
}

func TestAcquireExcludesAnotherHolder(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	clk := &fixedClock{t: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	a, b := newLock(c, "controller-a", clk), newLock(c, "controller-b", clk)

	got, err := a.Acquire(ctx, "Enrollment", "acme")
	if err != nil || !got {
		t.Fatalf("first Acquire = (%v, %v), want (true, nil)", got, err)
	}
	got, err = b.Acquire(ctx, "Enrollment", "acme")
	if err != nil {
		t.Fatalf("concurrent Acquire returned an error: %v", err)
	}
	if got {
		t.Fatal("controller-b got a held, valid lock: two concurrent tofu applies")
	}
	for i := 0; i < 3; i++ {
		clk.advance(5 * time.Second)
		if got, err := a.Acquire(ctx, "Enrollment", "acme"); err != nil || !got {
			t.Fatalf("renewal %d = (%v, %v), want (true, nil)", i, got, err)
		}
	}
}

func TestAcquireTakesOverAnAbandonedLock(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	clk := &fixedClock{t: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	dead, alive := newLock(c, "controller-dead", clk), newLock(c, "controller-alive", clk)

	if got, err := dead.Acquire(ctx, "Target", "metallb-acme"); err != nil || !got {
		t.Fatalf("initial Acquire: (%v, %v)", got, err)
	}
	clk.advance(20 * time.Second)
	if got, _ := alive.Acquire(ctx, "Target", "metallb-acme"); got {
		t.Fatal("took over a lock that is still valid")
	}
	clk.advance(20 * time.Second)
	if got, err := alive.Acquire(ctx, "Target", "metallb-acme"); err != nil || !got {
		t.Fatalf("takeover after expiry = (%v, %v), want (true, nil)", got, err)
	}

	var lease coordinationv1.Lease
	key := types.NamespacedName{Namespace: lockNamespace, Name: LeaseName("Target", "metallb-acme")}
	if err := c.Get(ctx, key, &lease); err != nil {
		t.Fatalf("reading the Lease: %v", err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "controller-alive" {
		t.Errorf("holder = %v, want controller-alive", lease.Spec.HolderIdentity)
	}
	if lease.Spec.LeaseTransitions == nil || *lease.Spec.LeaseTransitions != 1 {
		t.Errorf("leaseTransitions = %v, want 1 (trace of the dead controller)", lease.Spec.LeaseTransitions)
	}
}

func TestReleaseOnlyByHolder(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	clk := &fixedClock{t: time.Now()}
	a, b := newLock(c, "controller-a", clk), newLock(c, "controller-b", clk)

	if got, err := a.Acquire(ctx, "Enrollment", "edge"); err != nil || !got {
		t.Fatalf("Acquire: (%v, %v)", got, err)
	}
	if err := b.Release(ctx, "Enrollment", "edge"); err != nil {
		t.Fatalf("Release by a non-holder must be a no-op, error: %v", err)
	}
	holder, valid, err := a.HeldBy(ctx, "Enrollment", "edge")
	if err != nil {
		t.Fatalf("HeldBy: %v", err)
	}
	if holder != "controller-a" || !valid {
		t.Fatalf("after Release by b: holder=%q valid=%v, want controller-a/true", holder, valid)
	}
	if err := a.Release(ctx, "Enrollment", "edge"); err != nil {
		t.Fatalf("Release by the holder: %v", err)
	}
	var lease coordinationv1.Lease
	key := types.NamespacedName{Namespace: lockNamespace, Name: LeaseName("Enrollment", "edge")}
	if err := c.Get(ctx, key, &lease); !apierrors.IsNotFound(err) {
		t.Errorf("Lease still present after Release: err=%v", err)
	}
	if err := a.Release(ctx, "Enrollment", "edge"); err != nil {
		t.Errorf("want an idempotent Release, error: %v", err)
	}
	if got, err := b.Acquire(ctx, "Enrollment", "edge"); err != nil || !got {
		t.Fatalf("Acquire after release = (%v, %v), want (true, nil)", got, err)
	}
}

func TestConcurrentAcquireElectsExactlyOne(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	clk := &fixedClock{t: time.Now()}

	const n = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
		errs    []error
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		holder := "controller-" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l := newLock(c, holder, clk)
			got, err := l.Acquire(ctx, "Enrollment", "race")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if got {
				winners = append(winners, holder)
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		t.Errorf("error during the race: %v", err)
	}
	if len(winners) != 1 {
		t.Fatalf("%d winners (%v), want exactly 1", len(winners), winners)
	}
	holder, valid, err := newLock(c, "observer", clk).HeldBy(ctx, "Enrollment", "race")
	if err != nil {
		t.Fatalf("HeldBy: %v", err)
	}
	if holder != winners[0] || !valid {
		t.Errorf("recorded holder = %q (valid=%v), declared winner = %q", holder, valid, winners[0])
	}
}

func TestLeaseNameIsStableAndScoped(t *testing.T) {
	if a, b := LeaseName("Enrollment", "acme"), LeaseName("enrollment", "Acme"); a != b {
		t.Errorf("name is case-sensitive: %q vs %q", a, b)
	}
	if got := LeaseName("Enrollment", "acme"); got != "rondo-lock-enrollment-acme" {
		t.Errorf("LeaseName = %q, want rondo-lock-enrollment-acme", got)
	}
	if LeaseName("Enrollment", "acme") == LeaseName("Target", "acme") {
		t.Error("two kinds with the same name share their lock")
	}
}
