package dashboard

import (
	"io"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func pod(name string, age time.Duration) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns,
		Labels:            map[string]string{jobNameLabel: "helm-traefik"},
		CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
	}}
}

func TestPodLogsReadsTheNewestPodOfTheJob(t *testing.T) {
	cs := fake.NewClientset(pod("vieux", time.Hour), pod("neuf", 0))
	body, err := (&PodLogs{Clientset: cs, Namespace: ns}).JobLogs(t.Context(), "helm-traefik", 10)
	if err != nil {
		t.Fatalf("JobLogs: %v", err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestPodLogsNamesTheMissingPod(t *testing.T) {
	cs := fake.NewClientset()
	_, err := (&PodLogs{Clientset: cs, Namespace: ns}).JobLogs(t.Context(), "helm-traefik", 10)
	if err == nil || !strings.Contains(err.Error(), "helm-traefik") {
		t.Fatalf("error = %v, it must name the Job", err)
	}
}

func TestJobStateFollowsTheCounters(t *testing.T) {
	for _, tc := range []struct {
		status batchv1.JobStatus
		want   string
	}{
		{batchv1.JobStatus{}, "Pending"},
		{batchv1.JobStatus{Active: 1}, "Running"},
		{batchv1.JobStatus{Succeeded: 1}, "Succeeded"},
		{batchv1.JobStatus{Failed: 1}, "Failed"},
		{batchv1.JobStatus{Succeeded: 1, Failed: 2}, "Succeeded"},
	} {
		j := &batchv1.Job{ObjectMeta: meta("j"), Status: tc.status}
		if got := jobView(j).State; got != tc.want {
			t.Errorf("%+v -> %q, want %q", tc.status, got, tc.want)
		}
	}
	if got := jobView(&batchv1.Job{ObjectMeta: meta("j")}).Owner; got != "" {
		t.Errorf("owner = %q", got)
	}
}

func TestJobsAreSortedNewestFirst(t *testing.T) {
	in := []JobView{
		{Name: "b", StartedAt: "2026-01-01T00:00:00Z"},
		{Name: "c", StartedAt: "2026-02-01T00:00:00Z"},
		{Name: "a", StartedAt: "2026-01-01T00:00:00Z"},
	}
	sortJobs(in)
	if in[0].Name != "c" || in[1].Name != "a" || in[2].Name != "b" {
		t.Fatalf("order = %v", []string{in[0].Name, in[1].Name, in[2].Name})
	}
}

func TestAttemptViewToleratesAnUnfinishedAttempt(t *testing.T) {
	if attemptView(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	if a := attemptViews(nil); len(a) != 0 {
		t.Fatalf("empty history = %v", a)
	}
}
