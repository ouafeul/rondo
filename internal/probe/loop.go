package probe

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
	"rondo/internal/log"
)

const FieldManager = "rondo-probe"

type Prober struct {
	Client    client.Client
	Namespace string
	Interval  time.Duration
	Rand      *rand.Rand

	newReader func(context.Context, client.Client, *v1alpha1.Target, map[string]bool) (targetReader, error)
	readers   map[string]targetReader
}

const cacheSyncTimeout = 5 * time.Second

type passStats struct {
	enrollments int
	targets     int
	reads       int
	watches     int
	dur         time.Duration
}

const jitterFraction = 0.2

func (p *Prober) Run(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Info("rondo-probe started", "namespace", p.Namespace, "interval", interval,
		"jitter", fmt.Sprintf("±%.0f%%", jitterFraction*100))
	defer p.stopAll()
	for {
		if err := p.once(ctx); err != nil && ctx.Err() == nil {
			log.Warn("probe pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			log.Info("rondo-probe stopped")
			return nil
		case <-time.After(p.nextDelay(interval)):
		}
	}
}

func (p *Prober) nextDelay(interval time.Duration) time.Duration {
	span := float64(interval) * jitterFraction
	if span <= 0 {
		return interval
	}
	r := p.Rand
	var offset float64
	if r != nil {
		offset = (r.Float64()*2 - 1) * span
	} else {
		offset = (rand.Float64()*2 - 1) * span
	}
	d := time.Duration(float64(interval) + offset)
	if d < time.Second {
		d = time.Second
	}
	return d
}

func (p *Prober) once(ctx context.Context) error {
	st, err := p.pass(ctx)
	log.Info("probe pass done", "enrollments", st.enrollments, "targets", st.targets,
		"reads", st.reads, "watches", st.watches, "duration", st.dur.Round(time.Millisecond))
	return err
}

func (p *Prober) pass(ctx context.Context) (passStats, error) {
	start := time.Now()
	st := passStats{}
	var enrollments v1alpha1.EnrollmentList
	if err := p.Client.List(ctx, &enrollments, client.InNamespace(p.Namespace)); err != nil {
		st.dur = time.Since(start)
		return st, fmt.Errorf("listing Enrollments: %w", err)
	}
	st.enrollments = len(enrollments.Items)
	byTarget := inventoryNamespaces(enrollments.Items)
	probed := map[string]targetReader{}
	for i := range enrollments.Items {
		enr := &enrollments.Items[i]
		reads, err := p.assessEnrollment(ctx, enr, probed, byTarget)
		st.reads += reads
		if err != nil {
			log.Warn("Enrollment probe failed", "enrollment", enr.Name, "err", err)
		}
	}
	p.evict(probed)
	for _, r := range probed {
		st.watches += r.watches()
	}
	st.targets = len(probed)
	st.dur = time.Since(start)
	return st, nil
}

func inventoryNamespaces(enrollments []v1alpha1.Enrollment) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for i := range enrollments {
		enr := &enrollments[i]
		for _, e := range enr.Status.Inventory {
			if e.Namespace == "" {
				continue
			}
			t := enr.Status.ResolvedTarget
			if out[t] == nil {
				out[t] = map[string]bool{}
			}
			out[t][e.Namespace] = true
		}
	}
	return out
}

func (p *Prober) assessEnrollment(ctx context.Context, enr *v1alpha1.Enrollment,
	probed map[string]targetReader, byTarget map[string]map[string]bool) (int, error) {
	if len(enr.Status.Inventory) == 0 {
		return 0, nil
	}

	targetName := enr.Status.ResolvedTarget
	c, ok := probed[targetName]
	if !ok {
		var target v1alpha1.Target
		if err := p.Client.Get(ctx, types.NamespacedName{
			Namespace: enr.Namespace, Name: targetName,
		}, &target); err != nil {
			return 0, fmt.Errorf("reading Target %s: %w", targetName, err)
		}
		built, err := p.readerFor(ctx, &target, byTarget[targetName])
		if err != nil {
			return 0, p.publish(ctx, enr, v1alpha1.HealthUnknown,
				"target unreachable: "+err.Error())
		}
		probed[targetName] = built
		c = built
	}

	if err := c.stale(); err != nil {
		return 0, p.publish(ctx, enr, v1alpha1.HealthUnknown,
			"cache disconnected from the target: "+err.Error())
	}

	var rules []v1alpha1.HealthRule
	var comp v1alpha1.Component
	if err := p.Client.Get(ctx, types.NamespacedName{
		Namespace: enr.Namespace, Name: enr.Spec.Component,
	}, &comp); err != nil {
		log.Warn("Component unreadable: its declared kinds will not be taken into account",
			"enrollment", enr.Name, "component", enr.Spec.Component, "err", err)
	} else {
		rules = comp.Spec.Health
	}

	readCtx, cancel := context.WithTimeout(ctx, cacheSyncTimeout)
	defer cancel()
	health, message := AssessInventoryWith(readCtx, c, enr.Status.Inventory, rules)
	reads := len(enr.Status.Inventory)
	if health == enr.Status.Health && message == enr.Status.HealthMessage {
		return reads, nil
	}
	log.Info("health updated", "enrollment", enr.Name, "from", enr.Status.Health, "to", health)
	return reads, p.publish(ctx, enr, health, message)
}

func (p *Prober) publish(ctx context.Context, enr *v1alpha1.Enrollment, health, message string) error {
	patch := &unstructured.Unstructured{}
	patch.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Enrollment"))
	patch.SetNamespace(enr.Namespace)
	patch.SetName(enr.Name)
	if err := unstructured.SetNestedMap(patch.Object, map[string]any{
		"health":        health,
		"healthMessage": truncate(message, 2048),
	}, "status"); err != nil {
		return err
	}
	return p.Client.Status().Patch(ctx, patch, client.Apply,
		client.FieldOwner(FieldManager), client.ForceOwnership)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
