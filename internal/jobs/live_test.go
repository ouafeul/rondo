package jobs

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestLeaseTTLCoversTheJobBound(t *testing.T) {
	c := Config{Image: "toolbox:1"}

	if got := c.LeaseTTLFor(time.Hour); got <= time.Hour {
		t.Errorf("lease of %v for a Job bounded at 1h: it expires WHILE the Job it "+
			"protects is running, and its expiry becomes a licence to run twice", got)
	}
	if got, want := c.TimeoutFor(0), c.withDefaults().DefaultTimeout; got != want {
		t.Errorf("TimeoutFor(0) = %v, want the builder default %v", got, want)
	}
	if got := c.LeaseTTLFor(0); got <= c.TimeoutFor(0) {
		t.Errorf("lease of %v for a Job bounded at %v: same default, same coverage "+
			"gap", got, c.TimeoutFor(0))
	}
	if LeaseGrace < requeueMarginFloor {
		t.Errorf("LeaseGrace = %v: too short for the reconciler to have SEEN the Job again "+
			"before the lease expires (it only comes back every %v)",
			LeaseGrace, requeueMarginFloor)
	}
	if got, want := c.TimeoutFor(42*time.Minute), 42*time.Minute; got != want {
		t.Errorf("TimeoutFor(42m) = %v, want %v", got, want)
	}
}

const requeueMarginFloor = 15 * time.Second

type stubReader struct {
	client.Reader
	items []batchv1.Job
	err   error
}

func (s *stubReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	if s.err != nil {
		return s.err
	}
	jl, ok := list.(*batchv1.JobList)
	if !ok {
		return nil
	}
	jl.Items = s.items
	return nil
}

func jobWith(name string, cond batchv1.JobConditionType) batchv1.Job {
	j := batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if cond != "" {
		j.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue}}
	}
	return j
}

func TestLiveForSeesOnlyWhatIsStillRunning(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		items []batchv1.Job
		want  string
	}{
		{"no Job", nil, ""},
		{"only finished ones", []batchv1.Job{
			jobWith("a", batchv1.JobComplete), jobWith("b", batchv1.JobFailed),
		}, ""},
		{"one running among finished ones", []batchv1.Job{
			jobWith("a", batchv1.JobComplete), jobWith("b", ""),
		}, "b"},
		{"all running: the first one is enough", []batchv1.Job{
			jobWith("b", ""), jobWith("c", ""),
		}, "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LiveFor(ctx, &stubReader{items: tc.items}, "rondo-system", "Enrollment", "acme")
			if err != nil {
				t.Fatalf("LiveFor: %v", err)
			}
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("live Job %q returned although none is running: the resource "+
					"would stay blocked forever", got.Name)
			case tc.want != "" && got == nil:
				t.Fatalf("no live Job returned although %q is running: a second one would start "+
					"on the same resource", tc.want)
			case tc.want != "" && got.Name != tc.want:
				t.Fatalf("live Job = %q, want %q", got.Name, tc.want)
			}
		})
	}
}

func TestLiveForReturnsItsError(t *testing.T) {
	boom := &apiDown{}
	got, err := LiveFor(context.Background(), &stubReader{err: boom}, "rondo-system", "Enrollment", "acme")
	if err == nil {
		t.Fatal("unreadable inventory swallowed: the caller would believe no Job is running " +
			"and would launch a second one")
	}
	if got != nil {
		t.Errorf("Job returned despite the error: %v", got)
	}
}

type apiDown struct{}

func (apiDown) Error() string { return "apiserver unreachable" }
