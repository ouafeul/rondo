package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type conflictOnce struct {
	client.Client
	remaining int
	bump      func()
	updates   int
	gets      int
}

func (c *conflictOnce) Get(ctx context.Context, key client.ObjectKey, obj client.Object,
	opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *conflictOnce) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.remaining > 0 {
		c.remaining--
		if c.bump != nil {
			c.bump()
		}
		return apierrors.NewConflict(
			schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"},
			obj.GetName(), errors.New("the object has been modified"))
	}
	c.updates++
	return c.Client.Update(ctx, obj, opts...)
}

func leaseKey(kind, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: lockNamespace, Name: LeaseName(kind, name)}
}

func readLease(t *testing.T, c client.Client, key types.NamespacedName) coordinationv1.Lease {
	t.Helper()
	var l coordinationv1.Lease
	if err := c.Get(context.Background(), key, &l); err != nil {
		t.Fatalf("reading lease %s: %v", key.Name, err)
	}
	return l
}

func TestConflictOnOwnLeaseIsNotALostLease(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	const kind, name, holder = "Enrollment", "renewal-conflict", "rondo-controller-0"
	clk := &fixedClock{t: time.Now()}

	direct := newLock(c, holder, clk)
	if ok, err := direct.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("initial acquisition: ok=%v err=%v", ok, err)
	}
	key := leaseKey(kind, name)
	before := readLease(t, c, key)

	racer := &conflictOnce{Client: c, remaining: 1}
	lock := newLock(racer, holder, clk)
	clk.advance(10 * time.Second)

	ok, err := lock.Acquire(ctx, kind, name)
	if err != nil {
		t.Fatalf("Acquire after Conflict: %v", err)
	}
	if !ok {
		t.Fatal("the CURRENT HOLDER was refused renewal of its own lease on a mere " +
			"Conflict: the controller would set `Stalled`, naming itself as the " +
			"thief, on a healthy system")
	}
	if racer.updates != 1 {
		t.Errorf("%d Updates sent to the apiserver after the Conflict, want 1 "+
			"(a single retry)", racer.updates)
	}
	if racer.gets != 2 {
		t.Errorf("%d reads, want 2 (the initial read, then the re-read)", racer.gets)
	}
	after := readLease(t, c, key)
	if !after.Spec.RenewTime.After(before.Spec.RenewTime.Time) {
		t.Errorf("renewTime unchanged (%v -> %v): the lease was not renewed",
			before.Spec.RenewTime, after.Spec.RenewTime)
	}
	if got := *after.Spec.HolderIdentity; got != holder {
		t.Errorf("holder = %q, want %q", got, holder)
	}
}

func TestConflictAfterARealTakeoverLosesTheLease(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	const kind, name, holder = "Enrollment", "renewal-stolen", "rondo-controller-0"
	clk := &fixedClock{t: time.Now()}

	direct := newLock(c, holder, clk)
	if ok, err := direct.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("initial acquisition: ok=%v err=%v", ok, err)
	}
	key := leaseKey(kind, name)

	racer := &conflictOnce{Client: c, remaining: 1, bump: func() {
		var l coordinationv1.Lease
		if err := c.Get(ctx, key, &l); err != nil {
			t.Fatalf("concurrent read: %v", err)
		}
		other := "rondo-controller-1"
		l.Spec.HolderIdentity = &other
		if err := c.Update(ctx, &l); err != nil {
			t.Fatalf("concurrent takeover: %v", err)
		}
	}}
	lock := newLock(racer, holder, clk)

	ok, err := lock.Acquire(ctx, kind, name)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if ok {
		t.Fatal("the lease changed hands: acquisition must fail, otherwise two " +
			"concurrent runs start on the same tofu state")
	}
	if got := *readLease(t, c, key).Spec.HolderIdentity; got != "rondo-controller-1" {
		t.Errorf("holder = %q: the loser overwrote the winner", got)
	}
}

func TestConflictOnAVanishedLeaseIsNotAnError(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	const kind, name, holder = "Enrollment", "renewal-vanished", "rondo-controller-0"
	clk := &fixedClock{t: time.Now()}

	direct := newLock(c, holder, clk)
	if ok, err := direct.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("initial acquisition: ok=%v err=%v", ok, err)
	}
	key := leaseKey(kind, name)

	racer := &conflictOnce{Client: c, remaining: 1, bump: func() {
		l := readLease(t, c, key)
		if err := c.Delete(ctx, &l); err != nil {
			t.Fatalf("concurrent deletion: %v", err)
		}
	}}
	lock := newLock(racer, holder, clk)

	ok, err := lock.Acquire(ctx, kind, name)
	if err != nil {
		t.Fatalf("a vanished lease must not surface as an error: %v", err)
	}
	if ok {
		t.Fatal("acquisition reported as successful although the lease is gone")
	}
}

func TestConflictOnTakeoverOfAnAbandonedLeaseDoesNotReread(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	const kind, name = "Enrollment", "takeover-conflict"
	clk := &fixedClock{t: time.Now()}

	if ok, err := newLock(c, "old-holder", clk).Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("initial acquisition: ok=%v err=%v", ok, err)
	}
	clk.advance(time.Hour)

	racer := &conflictOnce{Client: c, remaining: 1}
	ok, err := newLock(racer, "new-holder", clk).Acquire(ctx, kind, name)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if ok {
		t.Fatal("the takeover must fail on Conflict: the other one won the race")
	}
	if racer.updates != 0 {
		t.Errorf("%d Updates sent: a lost takeover must not be replayed", racer.updates)
	}
	if racer.gets != 1 {
		t.Errorf("%d reads, want 1: a race lost on SOMEONE ELSE'S lease "+
			"must not be re-read", racer.gets)
	}
	if got := *readLease(t, c, leaseKey(kind, name)).Spec.HolderIdentity; got != "old-holder" {
		t.Errorf("holder = %q, want \"old-holder\": the loser overwrote the lease", got)
	}
}

func TestAcquireForFallsBackToDurationOnZeroBound(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()

	clk := &fixedClock{t: time.Now()}
	lock := newLock(c, "rondo-controller-0", clk)
	if ok, err := lock.AcquireFor(ctx, "Enrollment", "zero-bound", 0); err != nil || !ok {
		t.Fatalf("AcquireFor(0): ok=%v err=%v", ok, err)
	}
	if got := *readLease(t, c, leaseKey("Enrollment", "zero-bound")).Spec.LeaseDurationSeconds; got != 30 {
		t.Errorf("validity = %d s, want 30 (falls back to Duration)", got)
	}

	noDuration := &Lock{Client: c, Namespace: lockNamespace,
		Holder: "rondo-controller-0", Now: clk.now}
	if ok, err := noDuration.Acquire(ctx, "Enrollment", "no-duration"); err != nil || !ok {
		t.Fatalf("Acquire without Duration: ok=%v err=%v", ok, err)
	}
	if got := *readLease(t, c, leaseKey("Enrollment", "no-duration")).Spec.LeaseDurationSeconds; got != 120 {
		t.Errorf("default validity = %d s, want 120", got)
	}
}

func TestReleaseAndHeldByOnAMissingLease(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	clk := &fixedClock{t: time.Now()}
	lock := newLock(c, "rondo-controller-0", clk)

	if err := lock.Release(ctx, "Enrollment", "never-taken"); err != nil {
		t.Errorf("Release on a missing lease: %v", err)
	}
	holder, valid, err := lock.HeldBy(ctx, "Enrollment", "never-taken")
	if err != nil {
		t.Errorf("HeldBy on a missing lease: %v", err)
	}
	if holder != "" || valid {
		t.Errorf("HeldBy = (%q, %v), want (\"\", false)", holder, valid)
	}
}

func TestExpiredWithoutRenewTime(t *testing.T) {
	l := &Lock{Duration: time.Minute}
	if !l.expired(&coordinationv1.Lease{}) {
		t.Error("a lease without renewTime must be considered expired")
	}
}
