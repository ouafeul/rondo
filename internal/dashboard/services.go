package dashboard

import (
	"context"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *Server) Services(ctx context.Context) ([]ServiceView, error) {
	var deps appsv1.DeploymentList
	if err := s.Client.List(ctx, &deps, s.inNS()); err != nil {
		return nil, err
	}
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods, s.inNS()); err != nil {
		return nil, err
	}
	out := make([]ServiceView, 0, len(deps.Items))
	for i := range deps.Items {
		d := &deps.Items[i]
		out = append(out, serviceView(d, podsOf(d, pods.Items)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func podsOf(d *appsv1.Deployment, pods []corev1.Pod) []corev1.Pod {
	sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil || sel.Empty() {
		return nil
	}
	var out []corev1.Pod
	for i := range pods {
		if sel.Matches(labels.Set(pods[i].Labels)) {
			out = append(out, pods[i])
		}
	}
	return out
}

func serviceView(d *appsv1.Deployment, pods []corev1.Pod) ServiceView {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	v := ServiceView{
		Name: d.Name, Desired: desired, Ready: d.Status.ReadyReplicas,
		Updated: d.Status.UpdatedReplicas, Available: d.Status.AvailableReplicas,
		Pods: []PodView{},
	}
	if cs := d.Spec.Template.Spec.Containers; len(cs) > 0 {
		v.Image = cs[0].Image
		if i := strings.LastIndex(v.Image, ":"); i > strings.LastIndex(v.Image, "/") {
			v.Version = v.Image[i+1:]
		}
	}
	v.State, v.Message = serviceState(d, desired)
	for i := range pods {
		v.Pods = append(v.Pods, podView(&pods[i]))
	}
	sort.Slice(v.Pods, func(i, j int) bool { return v.Pods[i].Name < v.Pods[j].Name })
	return v
}

func serviceState(d *appsv1.Deployment, desired int32) (string, string) {
	msg := ""
	for _, c := range d.Status.Conditions {
		if c.Status != corev1.ConditionTrue && c.Message != "" {
			msg = c.Message
		}
	}
	switch {
	case desired > 0 && d.Status.AvailableReplicas == 0:
		return "Down", msg
	case d.Status.ObservedGeneration < d.Generation || d.Status.UpdatedReplicas < desired:
		return "Progressing", msg
	case d.Status.AvailableReplicas < desired:
		return "Degraded", msg
	}
	return "Healthy", ""
}

func podView(p *corev1.Pod) PodView {
	v := PodView{
		Name: p.Name, Phase: string(p.Status.Phase), Node: p.Spec.NodeName,
		Ready: len(p.Status.ContainerStatuses) > 0, StartedAt: stamp(p.Status.StartTime),
	}
	for _, cs := range p.Status.ContainerStatuses {
		v.Restarts += cs.RestartCount
		v.Ready = v.Ready && cs.Ready
		switch {
		case v.Reason != "":
		case cs.State.Waiting != nil:
			v.Reason = cs.State.Waiting.Reason
		case cs.State.Terminated != nil:
			v.Reason = cs.State.Terminated.Reason
		}
	}
	if v.Reason == "" {
		v.Reason = p.Status.Reason
	}
	return v
}

func (s *Server) servicePod(ctx context.Context, name string) (bool, error) {
	var deps appsv1.DeploymentList
	if err := s.Client.List(ctx, &deps, s.inNS()); err != nil {
		return false, err
	}
	var pod corev1.Pod
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	for i := range deps.Items {
		if len(podsOf(&deps.Items[i], []corev1.Pod{pod})) == 1 {
			return true, nil
		}
	}
	return false, nil
}
