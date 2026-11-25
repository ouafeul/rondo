package dashboard

import (
	"sort"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"rondo/api/v1alpha1"
	"rondo/internal/jobs"
)

func conditionStatus(conds []metav1.Condition, typ string) string {
	for i := range conds {
		if conds[i].Type == typ {
			return string(conds[i].Status)
		}
	}
	return string(metav1.ConditionUnknown)
}

func conditionMessage(conds []metav1.Condition, typ string) string {
	for i := range conds {
		if conds[i].Type == typ {
			return conds[i].Message
		}
	}
	return ""
}

func readyRow(name string, conds []metav1.Condition, phase string) Row {
	return Row{
		Name:    name,
		Ready:   conditionStatus(conds, v1alpha1.ConditionReady),
		Phase:   phase,
		Message: conditionMessage(conds, v1alpha1.ConditionReady),
	}
}

func summarize(kind string, rows []Row) Summary {
	s := Summary{Kind: kind, Total: len(rows), Rows: rows}
	for _, r := range rows {
		if r.Ready == string(metav1.ConditionTrue) {
			s.Ready++
		}
	}
	return s
}

func stamp(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func attemptView(a *v1alpha1.AttemptInfo) *AttemptView {
	if a == nil {
		return nil
	}
	return &AttemptView{
		JobName:    a.JobName,
		Result:     a.Result,
		Version:    a.Version,
		Message:    a.Message,
		StartedAt:  stamp(&a.StartedAt),
		FinishedAt: stamp(a.FinishedAt),
	}
}

func attemptViews(in []v1alpha1.AttemptInfo) []AttemptView {
	out := make([]AttemptView, 0, len(in))
	for i := range in {
		out = append(out, *attemptView(&in[i]))
	}
	return out
}

func enrollmentView(r *v1alpha1.Enrollment) EnrollmentView {
	return EnrollmentView{
		Name:                r.Name,
		Component:           r.Spec.Component,
		Target:              r.Status.ResolvedTarget,
		Phase:               string(r.Status.Phase),
		Ready:               conditionStatus(r.Status.Conditions, v1alpha1.ConditionReady),
		Message:             conditionMessage(r.Status.Conditions, v1alpha1.ConditionReady),
		Health:              r.Status.Health,
		HealthMessage:       r.Status.HealthMessage,
		ChartVersion:        r.Status.AppliedChartVersion,
		ValuesHash:          r.Status.AppliedValuesHash,
		InventoryCount:      r.Status.InventoryCount,
		Attempts:            r.Status.AttemptCount,
		ConsecutiveFailures: r.Status.ConsecutiveFailures,
		LastAttempt:         attemptView(r.Status.LastAttempt),
		History:             attemptViews(r.Status.History),
	}
}

const maxValueLen = 120

// targetView never reads a Secret: spec.values only, which the tree commits in clear.
func targetView(t *v1alpha1.Target) TargetView {
	values := make(map[string]string, len(t.Spec.Values))
	for k, v := range t.Spec.Values {
		if len(v) > maxValueLen {
			v = v[:maxValueLen] + "…"
		}
		values[k] = v
	}
	v := TargetView{
		Name:      t.Name,
		Type:      "vsphere",
		InCluster: t.Spec.InCluster(),
		Ready:     conditionStatus(t.Status.Conditions, v1alpha1.ConditionReady),
		Message:   conditionMessage(t.Status.Conditions, v1alpha1.ConditionReady),
		Roles:     t.Spec.Roles,
		Values:    values,
	}
	if k := t.Spec.Kubernetes; k != nil {
		v.Type, v.Server = "kubernetes", k.Server
	}
	return v
}

func componentView(c *v1alpha1.Component, enrolls []v1alpha1.Enrollment) ComponentView {
	v := ComponentView{
		Name:        c.Name,
		Archetype:   string(c.Spec.Archetype()),
		Namespace:   c.Spec.Namespace,
		Enrollments: []PlacementView{},
	}
	if c.Spec.Chart != nil {
		v.Chart, v.Version = c.Spec.Chart.OCI, c.Spec.Chart.Version
	}
	if c.Spec.VM != nil {
		for _, g := range c.Spec.VM.Groups {
			v.Groups = append(v.Groups, g.Name)
		}
	}
	for _, d := range c.Spec.DependsOn {
		v.DependsOn = append(v.DependsOn, d.Name)
	}
	for i := range enrolls {
		e := &enrolls[i]
		if e.Spec.Component != c.Name {
			continue
		}
		v.Enrollments = append(v.Enrollments, PlacementView{
			Name:   e.Name,
			Ready:  conditionStatus(e.Status.Conditions, v1alpha1.ConditionReady),
			Target: e.Status.ResolvedTarget,
		})
	}
	return v
}

func jobView(j *batchv1.Job) JobView {
	v := JobView{
		Name:      j.Name,
		Verb:      j.Labels[jobs.LabelVerb],
		State:     "Pending",
		Active:    j.Status.Active,
		Succeeded: j.Status.Succeeded,
		Failed:    j.Status.Failed,
		StartedAt: stamp(j.Status.StartTime),
		EndedAt:   stamp(j.Status.CompletionTime),
	}
	if kind, name := j.Labels[jobs.LabelOwnerKind], j.Labels[jobs.LabelOwnerName]; kind != "" || name != "" {
		v.Owner = strings.TrimPrefix(kind+"/"+name, "/")
	}
	switch {
	case j.Status.Succeeded > 0:
		v.State = "Succeeded"
	case j.Status.Failed > 0:
		v.State = "Failed"
	case j.Status.Active > 0:
		v.State = "Running"
	}
	return v
}

func sortJobs(in []JobView) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].StartedAt != in[j].StartedAt {
			return in[i].StartedAt > in[j].StartedAt
		}
		return in[i].Name < in[j].Name
	})
}
