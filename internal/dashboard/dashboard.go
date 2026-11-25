package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"rondo/api/v1alpha1"
	"rondo/internal/dashapi"
	"rondo/internal/jobs"
	"rondo/internal/kobj"
	"rondo/internal/log"
)

//go:embed static
var assets embed.FS

type LogSource interface {
	JobLogs(ctx context.Context, job string, tail int64) (io.ReadCloser, error)
	ServiceLogs(ctx context.Context, pod string, tail int64) (io.ReadCloser, error)
}

type Server struct {
	// Reader, not Client: the type itself says the service cannot write.
	Client    client.Reader
	Logs      LogSource
	Namespace string
	Addr      string
}

func (s *Server) withDefaults() {
	if s.Namespace == "" {
		s.Namespace = kobj.PlatformNamespace
	}
	if s.Addr == "" {
		s.Addr = ":8080"
	}
}

func (s *Server) Run(ctx context.Context) error {
	s.withDefaults()
	srv := &http.Server{Addr: s.Addr, Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Info("rondo-dashboard started", "namespace", s.Namespace, "addr", s.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	s.withDefaults()
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // the embedded tree is fixed at build time
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET "+dashapi.PathOverview, jsonHandler(s.Overview))
	mux.HandleFunc("GET "+dashapi.PathComponents, jsonHandler(s.Components))
	mux.HandleFunc("GET "+dashapi.PathEnrollments, jsonHandler(s.Enrollments))
	mux.HandleFunc("GET "+dashapi.PathTargets, jsonHandler(s.Targets))
	mux.HandleFunc("GET "+dashapi.PathJobs, jsonHandler(s.Jobs))
	mux.HandleFunc("GET "+dashapi.PathServices, jsonHandler(s.Services))
	mux.HandleFunc("GET "+dashapi.PathLogs, s.logs)
	return mux
}

func jsonHandler[T any](fn func(context.Context) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := fn(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Warn("JSON response truncated", "err", err)
		}
	}
}

func (s *Server) inNS() client.ListOption { return client.InNamespace(s.Namespace) }

// Overview, Components, Enrollments, Targets, Jobs and Services are the read model the web
// dashboard serves and `rondo tui` renders.
func (s *Server) Overview(ctx context.Context) ([]Summary, error) {
	var (
		targets v1alpha1.TargetList
		comps   v1alpha1.ComponentList
		enrolls v1alpha1.EnrollmentList
	)
	for _, l := range []client.ObjectList{&targets, &comps, &enrolls} {
		if err := s.Client.List(ctx, l, s.inNS()); err != nil {
			return nil, err
		}
	}

	rows := func(n int, at func(int) Row) []Row {
		out := make([]Row, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, at(i))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	return []Summary{
		summarize("Target", rows(len(targets.Items), func(i int) Row {
			t := &targets.Items[i]
			return readyRow(t.Name, t.Status.Conditions, "")
		})),
		summarize("Component", rows(len(comps.Items), func(i int) Row {
			c := &comps.Items[i]
			return Row{Name: c.Name, Ready: string(metav1.ConditionTrue), Phase: string(c.Spec.Archetype())}
		})),
		summarize("Enrollment", rows(len(enrolls.Items), func(i int) Row {
			e := &enrolls.Items[i]
			row := readyRow(e.Name, e.Status.Conditions, string(e.Status.Phase))
			if row.Phase == "" {
				row.Phase = e.Status.Health
			}
			return row
		})),
	}, nil
}

func (s *Server) Components(ctx context.Context) ([]ComponentView, error) {
	var comps v1alpha1.ComponentList
	var enrolls v1alpha1.EnrollmentList
	if err := s.Client.List(ctx, &comps, s.inNS()); err != nil {
		return nil, err
	}
	if err := s.Client.List(ctx, &enrolls, s.inNS()); err != nil {
		return nil, err
	}
	out := make([]ComponentView, 0, len(comps.Items))
	for i := range comps.Items {
		out = append(out, componentView(&comps.Items[i], enrolls.Items))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Server) Enrollments(ctx context.Context) ([]EnrollmentView, error) {
	var list v1alpha1.EnrollmentList
	if err := s.Client.List(ctx, &list, s.inNS()); err != nil {
		return nil, err
	}
	out := make([]EnrollmentView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, enrollmentView(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Server) Targets(ctx context.Context) ([]TargetView, error) {
	var list v1alpha1.TargetList
	if err := s.Client.List(ctx, &list, s.inNS()); err != nil {
		return nil, err
	}
	out := make([]TargetView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, targetView(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Server) Jobs(ctx context.Context) ([]JobView, error) {
	list, err := s.managedJobs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]JobView, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, jobView(&list.Items[i]))
	}
	sortJobs(out)
	return out, nil
}

func (s *Server) managedJobs(ctx context.Context) (*batchv1.JobList, error) {
	var list batchv1.JobList
	err := s.Client.List(ctx, &list, s.inNS(),
		client.MatchingLabels{jobs.LabelManagedBy: jobs.ManagedByValue})
	return &list, err
}

const maxTailLines = 2000

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	job, pod := r.URL.Query().Get("job"), r.URL.Query().Get("pod")
	if job == "" && pod == "" {
		http.Error(w, "parameter `job` or `pod` required", http.StatusBadRequest)
		return
	}
	if s.Logs == nil {
		http.Error(w, "log reading not configured", http.StatusServiceUnavailable)
		return
	}
	// Only controller Jobs and service pods, else any pod log of the namespace is readable.
	known, kind, name := false, "Job", job
	var err error
	if job != "" {
		known, err = s.managedJob(r.Context(), job)
	} else {
		kind, name = "pod", pod
		known, err = s.servicePod(r.Context(), pod)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !known {
		http.Error(w, fmt.Sprintf("%s %q unknown in %s", kind, name, s.Namespace), http.StatusNotFound)
		return
	}

	tail := int64(500)
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := parsePositive(v); err == nil {
			tail = min(n, maxTailLines)
		}
	}
	var body io.ReadCloser
	if job != "" {
		body, err = s.Logs.JobLogs(r.Context(), job, tail)
	} else {
		body, err = s.Logs.ServiceLogs(r.Context(), pod, tail)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, body); err != nil {
		log.Warn("log copy interrupted", kind, name, "err", err)
	}
}

func (s *Server) managedJob(ctx context.Context, name string) (bool, error) {
	known, err := s.managedJobs(ctx)
	if err != nil {
		return false, err
	}
	for i := range known.Items {
		if known.Items[i].Name == name {
			return true, nil
		}
	}
	return false, nil
}

func parsePositive(v string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid value %q", v)
	}
	return n, nil
}
