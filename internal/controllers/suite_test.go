package controllers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

var (
	testCfg *rest.Config
	nsSeq   atomic.Int64
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		requireEnvtestOrDie("internal/controllers")
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "manifests", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtestOrDie(pkg string) {
	if os.Getenv("RONDO_REQUIRE_ENVTEST") != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "RONDO_REQUIRE_ENVTEST=1 but KUBEBUILDER_ASSETS is empty: "+
		"the envtest tests of %s would skip silently.\n"+
		"Set KUBEBUILDER_ASSETS:\n"+
		"  go install sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.1\n"+
		"  export KUBEBUILDER_ASSETS=\"$(setup-envtest use -p path)\"\n", pkg)
	os.Exit(1)
}

type testHarness struct {
	ns         string
	client     client.Client
	mapper     apimeta.RESTMapper
	enrollment *EnrollmentReconciler
	target     *TargetReconciler
	lock       *jobs.Lock
	scheme     *runtime.Scheme
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS not set (setup-envtest)")
	}
	cfg := testCfg
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme client-go: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("platform scheme: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	ns := fmt.Sprintf("rondo-test-%d", nsSeq.Add(1))
	if err := c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}); err != nil {
		t.Fatalf("test namespace: %v", err)
	}

	jobCfg := jobs.Config{
		Namespace:         ns,
		Image:             "arti.local/rondo-docker-local/rondo-toolbox:0.1.0",
		ServiceAccount:    "rondo-exec",
		MirrorCAConfigMap: "mirror-ca",
		MirrorCredsSecret: "rondo-credentials",
	}
	lock := &jobs.Lock{Client: c, Namespace: ns, Holder: "controller-test", Duration: time.Minute}

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatalf("HTTP client for the RESTMapper: %v", err)
	}
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		t.Fatalf("RESTMapper: %v", err)
	}

	return &testHarness{
		ns:         ns,
		client:     c,
		mapper:     mapper,
		scheme:     s,
		lock:       lock,
		enrollment: &EnrollmentReconciler{Client: c, Scheme: s, Jobs: jobCfg, Lock: lock, Reader: c},
		target:     &TargetReconciler{Client: c, Scheme: s},
	}
}

func (h *testHarness) req(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: h.ns, Name: name}}
}

func (h *testHarness) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: h.ns, Name: name}
}

func (h *testHarness) createTarget(t *testing.T, name string, roles []string, values map[string]string) *v1alpha1.Target {
	t.Helper()
	target := &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec: v1alpha1.TargetSpec{
			Roles: roles, Values: values,
			Kubernetes: &v1alpha1.KubernetesCluster{InCluster: true},
		},
	}
	if err := h.client.Create(context.Background(), target); err != nil {
		t.Fatalf("creating Target %s: %v", name, err)
	}
	if _, err := h.target.Reconcile(context.Background(), h.req(name)); err != nil {
		t.Fatalf("reconciling Target %s: %v", name, err)
	}
	return target
}

func (h *testHarness) createComponent(t *testing.T, name, version string, vals string, dependsOn ...string) *v1alpha1.Component {
	t.Helper()
	comp := &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec: v1alpha1.ComponentSpec{
			Chart: &v1alpha1.ChartSource{
				OCI:     "oci://arti.local/rondo-helm-local/" + name,
				Version: version,
			},
			Namespace: name,
		},
	}
	if vals != "" {
		comp.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(vals)}
	}
	for _, d := range dependsOn {
		comp.Spec.DependsOn = append(comp.Spec.DependsOn, v1alpha1.LocalObjectReference{Name: d})
	}
	if err := h.client.Create(context.Background(), comp); err != nil {
		t.Fatalf("creating Component %s: %v", name, err)
	}
	return comp
}

func (h *testHarness) createEnrollment(t *testing.T, name, component, target string,
	overlay string, mutate func(*v1alpha1.Enrollment)) *v1alpha1.Enrollment {
	t.Helper()
	enr := &v1alpha1.Enrollment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Spec:       v1alpha1.EnrollmentSpec{Component: component, Target: target},
	}
	if overlay != "" {
		enr.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(overlay)}
	}
	if mutate != nil {
		mutate(enr)
	}
	if err := h.client.Create(context.Background(), enr); err != nil {
		t.Fatalf("creating Enrollment %s: %v", name, err)
	}
	return enr
}

func (h *testHarness) getEnrollment(t *testing.T, name string) *v1alpha1.Enrollment {
	t.Helper()
	var enr v1alpha1.Enrollment
	if err := h.client.Get(context.Background(),
		h.key(name), &enr); err != nil {
		t.Fatalf("reading Enrollment %s: %v", name, err)
	}
	return &enr
}

func (h *testHarness) jobsFor(t *testing.T, enrollment string) []batchv1.Job {
	t.Helper()
	return h.jobsForKind(t, "Enrollment", enrollment)
}

func (h *testHarness) jobsForKind(t *testing.T, kind, name string) []batchv1.Job {
	t.Helper()
	var list batchv1.JobList
	if err := h.client.List(context.Background(), &list, client.InNamespace(h.ns),
		client.MatchingLabels{jobs.LabelOwnerKind: kind, jobs.LabelOwnerName: name}); err != nil {
		t.Fatalf("listing Jobs: %v", err)
	}
	return list.Items
}

func conditionOf(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

func requireCondition(t *testing.T, conds []metav1.Condition, condType, wantStatus string) string {
	t.Helper()
	c := conditionOf(conds, condType)
	if c == nil {
		t.Fatalf("condition %s missing (present: %v)", condType, conditionTypes(conds))
	}
	if string(c.Status) != wantStatus {
		t.Fatalf("condition %s = %s (reason=%s, message=%q), want %s",
			condType, c.Status, c.Reason, c.Message, wantStatus)
	}
	return c.Message
}

func conditionTypes(conds []metav1.Condition) []string {
	out := make([]string, 0, len(conds))
	for _, c := range conds {
		out = append(out, c.Type+"="+string(c.Status))
	}
	return out
}

func (h *testHarness) finishJob(t *testing.T, name string, succeeded bool) {
	t.Helper()
	ctx := context.Background()
	now := metav1.Now()

	interim := batchv1.JobConditionType("SuccessCriteriaMet")
	final, reason := batchv1.JobComplete, "CompletionsReached"
	if !succeeded {
		interim = batchv1.JobConditionType("FailureTarget")
		final, reason = batchv1.JobFailed, "BackoffLimitExceeded"
	}
	h.patchJobStatus(t, name, func(job *batchv1.Job) {
		job.Status.StartTime = &now
		job.Status.Conditions = []batchv1.JobCondition{{
			Type: interim, Status: corev1.ConditionTrue, Reason: reason,
			Message: "simulated by the test", LastTransitionTime: now,
		}}
	})

	h.patchJobStatus(t, name, func(job *batchv1.Job) {
		if succeeded {
			job.Status.CompletionTime = &now
			job.Status.Succeeded = 1
		} else {
			job.Status.Failed = 1
		}
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type: final, Status: corev1.ConditionTrue, Reason: reason,
			Message: "simulated by the test", LastTransitionTime: now,
		})
	})
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}

func (h *testHarness) patchJobStatus(t *testing.T, name string, mutate func(*batchv1.Job)) {
	t.Helper()
	ctx := context.Background()
	var job batchv1.Job
	if err := h.client.Get(ctx, h.key(name), &job); err != nil {
		t.Fatalf("reading Job %s: %v", name, err)
	}
	mutate(&job)
	if err := h.client.Status().Update(ctx, &job); err != nil {
		t.Fatalf("updating the status of Job %s: %v", name, err)
	}
}

type laggingJobCache struct {
	client.Client
	hidden *atomic.Bool
}

var jobsResource = schema.GroupResource{Group: "batch", Resource: "jobs"}

func (c *laggingJobCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object,
	opts ...client.GetOption) error {
	if _, ok := obj.(*batchv1.Job); ok && c.hidden.Load() {
		return apierrors.NewNotFound(jobsResource, key.Name)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *laggingJobCache) List(ctx context.Context, list client.ObjectList,
	opts ...client.ListOption) error {
	if jl, ok := list.(*batchv1.JobList); ok && c.hidden.Load() {
		jl.Items = nil
		return nil
	}
	return c.Client.List(ctx, list, opts...)
}

func deleteLease(t *testing.T, h *testHarness, kind, name string) {
	t.Helper()
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Namespace: h.ns, Name: jobs.LeaseName(kind, name),
	}}
	if err := h.client.Delete(context.Background(), lease); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("deleting lease %s: %v", lease.Name, err)
	}
}

func namesOf(list []batchv1.Job) []string {
	out := make([]string, 0, len(list))
	for _, j := range list {
		out = append(out, j.Name)
	}
	return out
}
