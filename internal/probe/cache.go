package probe

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
	"rondo/internal/kubeclient"
	"rondo/internal/log"
)

type targetReader interface {
	client.Reader
	stale() error
	watches() int
	covers(namespaces map[string]bool) bool
	stop()
}

type targetCache struct {
	reader     client.Reader
	cancel     context.CancelFunc
	namespaces map[string]bool

	disconnected atomic.Pointer[string]

	mu     sync.Mutex
	gvks   map[schema.GroupVersionKind]bool
	denied map[string]bool
}

func newTargetCache(ctx context.Context, local client.Client, target *v1alpha1.Target,
	namespaces map[string]bool) (targetReader, error) {
	cfg, err := kubeclient.RESTConfigForTarget(ctx, local, target)
	if err != nil {
		return nil, err
	}
	tc := &targetCache{gvks: map[schema.GroupVersionKind]bool{}, namespaces: namespaces}
	strip := cache.TransformStripManagedFields()
	opts := cache.Options{
		DefaultWatchErrorHandler: func(_ context.Context, r *toolscache.Reflector, err error) {
			tc.onWatchError(target.Name, r.TypeDescription(), err)
		},
		DefaultTransform: func(obj any) (any, error) {
			tc.disconnected.Store(nil)
			return strip(obj)
		},
	}
	if len(namespaces) > 0 {
		opts.DefaultNamespaces = map[string]cache.Config{}
		for ns := range namespaces {
			opts.DefaultNamespaces[ns] = cache.Config{}
		}
	}
	c, err := cache.New(cfg, opts)
	if err != nil {
		return nil, fmt.Errorf("cache for target %s: %w", target.Name, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	tc.reader, tc.cancel = c, cancel
	go func() {
		if err := c.Start(cctx); err != nil && cctx.Err() == nil {
			tc.setDisconnected(err.Error())
			log.Warn("target cache stopped", "target", target.Name, "err", err)
		}
	}()
	return tc, nil
}

func (t *targetCache) onWatchError(targetName, desc string, err error) {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		if t.noteDenied(desc) {
			log.Warn("kind unreadable on a target: insufficient permissions",
				"target", targetName, "kind", desc, "err", err)
		}
		return
	}
	t.setDisconnected(fmt.Sprintf("%s: %v", desc, err))
}

func (t *targetCache) noteDenied(desc string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.denied[desc] {
		return false
	}
	t.denied[desc] = true
	return true
}

func (t *targetCache) setDisconnected(cause string) { t.disconnected.Store(&cause) }

func (t *targetCache) stale() error {
	if cause := t.disconnected.Load(); cause != nil {
		return fmt.Errorf("%s", *cause)
	}
	return nil
}

func (t *targetCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object,
	opts ...client.GetOption) error {
	t.note(obj.GetObjectKind().GroupVersionKind())
	return t.reader.Get(ctx, key, obj, opts...)
}

func (t *targetCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return t.reader.List(ctx, list, opts...)
}

func (t *targetCache) note(gvk schema.GroupVersionKind) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gvks[gvk] = true
}

func (t *targetCache) watches() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.gvks)
}

func (t *targetCache) covers(namespaces map[string]bool) bool {
	for ns := range namespaces {
		if !t.namespaces[ns] {
			return false
		}
	}
	return true
}

func (t *targetCache) stop() { t.cancel() }

func (p *Prober) readerFor(ctx context.Context, target *v1alpha1.Target,
	namespaces map[string]bool) (targetReader, error) {
	if r, ok := p.readers[target.Name]; ok {
		if r.covers(namespaces) {
			return r, nil
		}
		log.Info("target cache rebuilt: new namespace in an inventory",
			"target", target.Name)
		r.stop()
		delete(p.readers, target.Name)
	}
	build := p.newReader
	if build == nil {
		build = newTargetCache
	}
	r, err := build(ctx, p.Client, target, namespaces)
	if err != nil {
		return nil, err
	}
	if p.readers == nil {
		p.readers = map[string]targetReader{}
	}
	p.readers[target.Name] = r
	return r, nil
}

func (p *Prober) evict(probed map[string]targetReader) {
	for name, r := range p.readers {
		if _, ok := probed[name]; !ok {
			r.stop()
			delete(p.readers, name)
		}
	}
}

func (p *Prober) stopAll() { p.evict(nil) }
