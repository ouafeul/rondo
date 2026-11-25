package jobs

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type staleLeaseCache struct {
	client.Client
	snapshot map[string]*coordinationv1.Lease
}

func (c *staleLeaseCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object,
	opts ...client.GetOption) error {
	if lease, ok := obj.(*coordinationv1.Lease); ok {
		if snap, held := c.snapshot[key.Name]; held {
			snap.DeepCopyInto(lease)
			return nil
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func snapshotOf(t *testing.T, c client.Client, key client.ObjectKey) *coordinationv1.Lease {
	t.Helper()
	var lease coordinationv1.Lease
	if err := c.Get(context.Background(), key, &lease); err != nil {
		t.Fatalf("reading lease %s: %v", key.Name, err)
	}
	return &lease
}

func TestLeaseReadFromLaggingCacheIsNotALostLease(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	const kind, name, holder = "Enrollment", "lease-cache", "rondo-controller-0"

	direct := &Lock{Client: c, Namespace: lockNamespace, Holder: holder, Duration: time.Minute}
	if ok, err := direct.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("initial lease acquisition: ok=%v err=%v", ok, err)
	}
	var lease coordinationv1.Lease
	key := client.ObjectKey{Namespace: lockNamespace, Name: LeaseName(kind, name)}
	if err := c.Get(ctx, key, &lease); err != nil {
		t.Fatalf("reading the lease: %v", err)
	}
	stale := snapshotOf(t, c, key)
	if ok, err := direct.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("renewal: ok=%v err=%v", ok, err)
	}
	if err := c.Get(ctx, key, &lease); err != nil {
		t.Fatalf("re-reading the lease: %v", err)
	}
	if lease.ResourceVersion == stale.ResourceVersion {
		t.Fatal("resourceVersion did not change: the test would measure nothing")
	}

	cached := &Lock{
		Client: &staleLeaseCache{Client: c,
			snapshot: map[string]*coordinationv1.Lease{key.Name: stale}},
		Reader:    c,
		Namespace: lockNamespace,
		Holder:    holder,
		Duration:  time.Minute,
	}
	held, err := cached.Acquire(ctx, kind, name)
	if err != nil {
		t.Fatalf("renewal with a lagging cache: %v", err)
	}
	if !held {
		t.Fatalf("the CURRENT HOLDER %q was refused renewal of its own lease "+
			"because its cache lagged one pass behind.\n"+
			"The controller then sets `Stalled` with \"mutual-exclusion lease LOST "+
			"to %q\", naming itself as the thief, on a perfectly healthy "+
			"system. One false Stalled ruins the credibility of every real one.",
			holder, holder)
	}

	other := &Lock{
		Client: &staleLeaseCache{Client: c,
			snapshot: map[string]*coordinationv1.Lease{key.Name: stale}},
		Reader:    c,
		Namespace: lockNamespace,
		Holder:    "rondo-controller-1",
		Duration:  time.Minute,
	}
	if ok, err := other.Acquire(ctx, kind, name); err != nil || ok {
		t.Fatalf("a second controller got a VALID lease held by %q: "+
			"ok=%v err=%v, mutual exclusion is gone", holder, ok, err)
	}
}

func TestHeldByDoesNotReadTheCache(t *testing.T) {
	c := leaseEnv(t)
	ctx := context.Background()
	const kind, name = "Enrollment", "lease-heldby"

	t0 := time.Now()
	first := &Lock{Client: c, Namespace: lockNamespace, Holder: "old-holder",
		Duration: time.Second, Now: func() time.Time { return t0 }}
	if ok, err := first.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("initial acquisition: ok=%v err=%v", ok, err)
	}
	key := client.ObjectKey{Namespace: lockNamespace, Name: LeaseName(kind, name)}
	stale := snapshotOf(t, c, key)

	second := &Lock{Client: c, Namespace: lockNamespace, Holder: "new-holder",
		Duration: time.Minute, Now: func() time.Time { return t0.Add(10 * time.Second) }}
	if ok, err := second.Acquire(ctx, kind, name); err != nil || !ok {
		t.Fatalf("taking over the expired lease: ok=%v err=%v", ok, err)
	}

	observer := &Lock{
		Client: &staleLeaseCache{Client: c,
			snapshot: map[string]*coordinationv1.Lease{key.Name: stale}},
		Reader:    c,
		Namespace: lockNamespace,
		Holder:    "observer",
		Duration:  time.Minute,
	}
	holder, _, err := observer.HeldBy(ctx, kind, name)
	if err != nil {
		t.Fatalf("HeldBy: %v", err)
	}
	if holder != "new-holder" {
		t.Errorf("HeldBy = %q, want \"new-holder\": the name shown in operator "+
			"messages comes from the cache, so it sends people looking for the wrong "+
			"controller", holder)
	}
}
