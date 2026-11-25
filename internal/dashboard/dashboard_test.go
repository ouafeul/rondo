package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

const ns = "rondo-system"

func meta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns}
}

func ready(status metav1.ConditionStatus, msg string) []metav1.Condition {
	return []metav1.Condition{{
		Type:    v1alpha1.ConditionReady,
		Status:  status,
		Reason:  "Test",
		Message: msg,
	}}
}

func newServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &Server{Client: c, Namespace: ns, Logs: &stubLogs{body: "log line"}}
}

type stubLogs struct {
	body string
	err  error
	tail int64
}

func (s *stubLogs) JobLogs(_ context.Context, _ string, tail int64) (io.ReadCloser, error) {
	s.tail = tail
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}

func (s *stubLogs) ServiceLogs(ctx context.Context, pod string, tail int64) (io.ReadCloser, error) {
	return s.JobLogs(ctx, pod, tail)
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding: %v (%s)", err, rec.Body.String())
	}
	return out
}

func fixture() []client.Object {
	return []client.Object{
		&v1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "management", Namespace: ns},
			Spec: v1alpha1.TargetSpec{
				Roles:      []string{"edge"},
				Kubernetes: &v1alpha1.KubernetesCluster{InCluster: true},
				Values: map[string]string{
					"clusterDomain": "cluster.example.local",
					"mirrorCA":      strings.Repeat("A", 400),
				},
			},
			Status: v1alpha1.TargetStatus{Conditions: ready(metav1.ConditionTrue, "")},
		},
		&v1alpha1.Component{
			ObjectMeta: meta("traefik"),
			Spec: v1alpha1.ComponentSpec{
				Chart:     &v1alpha1.ChartSource{OCI: "oci://r/traefik", Version: "41.0.2"},
				Namespace: "traefik",
				DependsOn: []v1alpha1.LocalObjectReference{{Name: "metallb"}},
			},
		},
		&v1alpha1.Component{
			ObjectMeta: meta("demo-mft"),
			Spec: v1alpha1.ComponentSpec{
				VM: &v1alpha1.VMSpec{Groups: []v1alpha1.VMGroup{{Name: "front"}, {Name: "back"}}},
			},
		},
		&v1alpha1.Enrollment{
			ObjectMeta: meta("traefik-management"),
			Spec:       v1alpha1.EnrollmentSpec{Component: "traefik", Target: "edge"},
			Status: v1alpha1.EnrollmentStatus{
				ResolvedTarget:      "management",
				Conditions:          ready(metav1.ConditionTrue, "ok"),
				Health:              v1alpha1.HealthHealthy,
				AppliedChartVersion: "41.0.2",
				AppliedValuesHash:   "0123456789abcdef",
				InventoryCount:      12,
				AttemptCount:        2,
				Phase:               v1alpha1.PhaseInstall,
				LastAttempt: &v1alpha1.AttemptInfo{
					JobName: "rel-j", Result: "Succeeded", StartedAt: metav1.Now(),
				},
				History: []v1alpha1.AttemptInfo{
					{JobName: "rel-j0", Result: "Failed", StartedAt: metav1.Now()},
				},
			},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: "helm-traefik", Namespace: ns,
				Labels: map[string]string{
					jobs.LabelManagedBy: jobs.ManagedByValue,
					jobs.LabelVerb:      string(jobs.VerbHelmApply),
					jobs.LabelOwnerKind: "Enrollment",
					jobs.LabelOwnerName: "traefik-management",
				},
			},
			Status: batchv1.JobStatus{Active: 1, StartTime: &metav1.Time{Time: metav1.Now().Time}},
		},
		&batchv1.Job{ObjectMeta: meta("etranger")},
	}
}

func TestOverviewCountsEveryKind(t *testing.T) {
	srv := newServer(t, fixture()...)
	got := decode[[]Summary](t, get(t, srv, "/api/overview"))

	want := map[string]int{"Target": 1, "Component": 2, "Enrollment": 1}
	if len(got) != len(want) {
		t.Fatalf("%d kinds, want %d", len(got), len(want))
	}
	for _, s := range got {
		if n, ok := want[s.Kind]; !ok || n != s.Total || len(s.Rows) != s.Total {
			t.Errorf("%s: total %d (%d rows), want %d", s.Kind, s.Total, len(s.Rows), n)
		}
		if s.Kind == "Enrollment" && s.Ready != 1 {
			t.Errorf("Enrollment Ready=%d", s.Ready)
		}
	}
}

func TestComponentsCarryTheirEnrollments(t *testing.T) {
	srv := newServer(t, fixture()...)
	got := decode[[]ComponentView](t, get(t, srv, "/api/components"))

	if len(got) != 2 || got[0].Name != "demo-mft" {
		t.Fatalf("components = %+v", got)
	}
	vm, chart := got[0], got[1]
	if vm.Archetype != string(v1alpha1.ArchetypeVM) || len(vm.Groups) != 2 || len(vm.Enrollments) != 0 {
		t.Errorf("VM component = %+v", vm)
	}
	if chart.Version != "41.0.2" || chart.Namespace != "traefik" || chart.DependsOn[0] != "metallb" {
		t.Errorf("chart component = %+v", chart)
	}
	if len(chart.Enrollments) != 1 || chart.Enrollments[0].Target != "management" {
		t.Errorf("traefik enrollments = %+v", chart.Enrollments)
	}
}

func TestEnrollmentsExposeHealthAndAttempts(t *testing.T) {
	srv := newServer(t, fixture()...)
	got := decode[[]EnrollmentView](t, get(t, srv, "/api/enrollments"))

	if len(got) != 1 {
		t.Fatalf("%d enrollments", len(got))
	}
	r := got[0]
	if r.Health != v1alpha1.HealthHealthy || r.InventoryCount != 12 || r.Attempts != 2 || r.Target != "management" {
		t.Errorf("enrollment = %+v", r)
	}
	if r.LastAttempt == nil || r.LastAttempt.Result != "Succeeded" || len(r.History) != 1 {
		t.Errorf("attempts = %+v / %+v", r.LastAttempt, r.History)
	}
}

func TestTargetsTruncateLongValuesAndKeepRoles(t *testing.T) {
	srv := newServer(t, fixture()...)
	got := decode[[]TargetView](t, get(t, srv, "/api/targets"))

	if len(got) != 1 {
		t.Fatalf("targets = %+v", got)
	}
	tgt := got[0]
	if tgt.Roles[0] != "edge" || !tgt.InCluster || tgt.Type != "kubernetes" {
		t.Errorf("target = %+v", tgt)
	}
	if ca := tgt.Values["mirrorCA"]; len([]rune(ca)) != maxValueLen+1 {
		t.Errorf("CA not truncated: %d runes", len([]rune(ca)))
	}
}

func TestJobsAreLimitedToTheControllersOwn(t *testing.T) {
	srv := newServer(t, fixture()...)
	got := decode[[]JobView](t, get(t, srv, "/api/jobs"))

	if len(got) != 1 {
		t.Fatalf("jobs = %+v, only the rondo-controller one must show", got)
	}
	j := got[0]
	if j.Verb != string(jobs.VerbHelmApply) || j.Owner != "Enrollment/traefik-management" {
		t.Errorf("job = %+v", j)
	}
	if j.State != "Running" {
		t.Errorf("state = %q", j.State)
	}
}

func TestLogsRefuseAJobTheControllerDoesNotOwn(t *testing.T) {
	srv := newServer(t, fixture()...)
	if rec := get(t, srv, "/api/logs?job=etranger"); rec.Code != http.StatusNotFound {
		t.Fatalf("code %d, a Job outside rondo-controller must be refused", rec.Code)
	}
	if rec := get(t, srv, "/api/logs"); rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d without a job parameter", rec.Code)
	}
}

func TestLogsServeThePodOutput(t *testing.T) {
	srv := newServer(t, fixture()...)
	rec := get(t, srv, "/api/logs?job=helm-traefik&tail=9999")
	if rec.Code != http.StatusOK || rec.Body.String() != "log line" {
		t.Fatalf("code %d, body %q", rec.Code, rec.Body.String())
	}
	if tail := srv.Logs.(*stubLogs).tail; tail != maxTailLines {
		t.Errorf("tail = %d, want %d", tail, maxTailLines)
	}
}

func TestLogsReportTheReaderFailure(t *testing.T) {
	srv := newServer(t, fixture()...)
	srv.Logs = &stubLogs{err: errors.New("pod gone")}
	if rec := get(t, srv, "/api/logs?job=helm-traefik"); rec.Code != http.StatusBadGateway {
		t.Fatalf("code %d", rec.Code)
	}

	srv.Logs = nil
	if rec := get(t, srv, "/api/logs?job=helm-traefik"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d without a log reader", rec.Code)
	}
}

func TestWriteMethodsAreNotRouted(t *testing.T) {
	srv := newServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/api/enrollments", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/enrollments -> %d, want 405", method, rec.Code)
		}
	}
}

func TestPageAndHealthzAreServed(t *testing.T) {
	srv := newServer(t)
	if rec := get(t, srv, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	rec := get(t, srv, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("page = %d, %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, srv, "/app.js"); rec.Code != http.StatusOK {
		t.Errorf("app.js = %d", rec.Code)
	}
}

func TestListFailureIsNotAnEmptyPlatform(t *testing.T) {
	srv := newServer(t)
	srv.Client = failingReader{}
	for _, path := range []string{"/api/overview", "/api/components", "/api/enrollments",
		"/api/targets", "/api/jobs", "/api/logs?job=x"} {
		if rec := get(t, srv, path); rec.Code != http.StatusBadGateway {
			t.Errorf("%s -> %d, want 502", path, rec.Code)
		}
	}
}

type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("apiserver unreachable")
}

func TestNewestPodWins(t *testing.T) {
	older := metav1.NewTime(metav1.Now().Add(-time.Hour))
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "vieux", CreationTimestamp: older}},
		{ObjectMeta: metav1.ObjectMeta{Name: "neuf", CreationTimestamp: metav1.Now()}},
	}
	if p := newestPod(pods); p == nil || p.Name != "neuf" {
		t.Fatalf("pod picked = %+v", p)
	}
	if newestPod(nil) != nil {
		t.Error("no pod must give nil")
	}
}

func TestDefaultsApplyWithoutConfiguration(t *testing.T) {
	s := &Server{}
	s.withDefaults()
	if s.Namespace != "rondo-system" || s.Addr != ":8080" {
		t.Fatalf("defaults = %q %q", s.Namespace, s.Addr)
	}
}
