package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const leasePrefix = "rondo-lock-"

type Lock struct {
	Client client.Client
	// Uncached: a stale resourceVersion would read our own lease as lost.
	Reader    client.Reader
	Namespace string
	Holder    string
	Duration  time.Duration
	Now       func() time.Time
}

func (l *Lock) reader() client.Reader {
	if l.Reader != nil {
		return l.Reader
	}
	return l.Client
}

func (l *Lock) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Lock) duration() time.Duration {
	if l.Duration <= 0 {
		return 2 * time.Minute
	}
	return l.Duration
}

func LeaseName(kind, name string) string {
	return leasePrefix + strings.ToLower(kind) + "-" + strings.ToLower(name)
}

func (l *Lock) Acquire(ctx context.Context, kind, name string) (bool, error) {
	return l.AcquireFor(ctx, kind, name, l.duration())
}

func (l *Lock) AcquireFor(ctx context.Context, kind, name string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = l.duration()
	}
	key := types.NamespacedName{Namespace: l.Namespace, Name: LeaseName(kind, name)}
	now := metav1.NewMicroTime(l.now())
	secs := int32(ttl.Seconds())

	var lease coordinationv1.Lease
	err := l.reader().Get(ctx, key, &lease)
	switch {
	case apierrors.IsNotFound(err):
		lease = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
				Labels: map[string]string{
					LabelManagedBy: ManagedByValue,
					LabelOwnerKind: kind,
					LabelOwnerName: name,
				},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &l.Holder,
				LeaseDurationSeconds: &secs,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		if err := l.Client.Create(ctx, &lease); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return false, nil
			}
			return false, fmt.Errorf("creating Lease %s: %w", key.Name, err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("reading Lease %s: %w", key.Name, err)
	}

	held := ""
	if lease.Spec.HolderIdentity != nil {
		held = *lease.Spec.HolderIdentity
	}
	mine := held == l.Holder

	if !mine && !l.expired(&lease) {
		return false, nil
	}

	if !mine {
		lease.Spec.HolderIdentity = &l.Holder
		lease.Spec.AcquireTime = &now
		transitions := int32(1)
		if lease.Spec.LeaseTransitions != nil {
			transitions = *lease.Spec.LeaseTransitions + 1
		}
		lease.Spec.LeaseTransitions = &transitions
	}
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = &secs
	if err := l.Client.Update(ctx, &lease); err != nil {
		if !apierrors.IsConflict(err) {
			return false, fmt.Errorf("updating Lease %s: %w", key.Name, err)
		}
		if !mine {
			return false, nil
		}
		return l.retryRenew(ctx, key, now, secs)
	}
	return true, nil
}

func (l *Lock) retryRenew(ctx context.Context, key types.NamespacedName,
	now metav1.MicroTime, secs int32) (bool, error) {
	var lease coordinationv1.Lease
	if err := l.reader().Get(ctx, key, &lease); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("re-reading Lease %s: %w", key.Name, err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.Holder {
		return false, nil
	}
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = &secs
	if err := l.Client.Update(ctx, &lease); err != nil {
		if apierrors.IsConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("updating Lease %s: %w", key.Name, err)
	}
	return true, nil
}

func (l *Lock) expired(lease *coordinationv1.Lease) bool {
	if lease.Spec.RenewTime == nil {
		return true
	}
	d := l.duration()
	if lease.Spec.LeaseDurationSeconds != nil && *lease.Spec.LeaseDurationSeconds > 0 {
		d = time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
	}
	return l.now().After(lease.Spec.RenewTime.Add(d))
}

func (l *Lock) Release(ctx context.Context, kind, name string) error {
	key := types.NamespacedName{Namespace: l.Namespace, Name: LeaseName(kind, name)}
	var lease coordinationv1.Lease
	if err := l.reader().Get(ctx, key, &lease); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading Lease %s: %w", key.Name, err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.Holder {
		return nil
	}
	if err := l.Client.Delete(ctx, &lease, client.Preconditions{
		UID:             &lease.UID,
		ResourceVersion: &lease.ResourceVersion,
	}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return nil
		}
		return fmt.Errorf("deleting Lease %s: %w", key.Name, err)
	}
	return nil
}

func (l *Lock) HeldBy(ctx context.Context, kind, name string) (holder string, valid bool, err error) {
	key := types.NamespacedName{Namespace: l.Namespace, Name: LeaseName(kind, name)}
	var lease coordinationv1.Lease
	if err := l.reader().Get(ctx, key, &lease); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}
	return holder, !l.expired(&lease), nil
}
