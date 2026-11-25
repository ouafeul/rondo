package dashboard

import (
	"net/http"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func deployment(name, image string, desired, updated, available int32) *appsv1.Deployment {
	labels := map[string]string{"app.kubernetes.io/name": name}
	d := &appsv1.Deployment{
		ObjectMeta: meta(name),
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: available, UpdatedReplicas: updated, AvailableReplicas: available,
		},
	}
	if available == 0 {
		d.Status.Conditions = []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse,
			Message: "Deployment does not have minimum availability.",
		}}
	}
	return d
}

func servicePodObj(name string, labels map[string]string, ready bool, restarts int32, waiting string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: meta(name), Spec: corev1.PodSpec{NodeName: "k3s-server-1"}}
	p.Labels = labels
	p.Status.Phase = corev1.PodRunning
	cs := corev1.ContainerStatus{Name: "c", Ready: ready, RestartCount: restarts}
	if waiting != "" {
		cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: waiting}
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
	return p
}

func servicesFixture() []client.Object {
	return []client.Object{
		deployment("rondo-controller", "reg/rondo-docker-local/rondo-controller:edge-main-9c481a63", 1, 1, 1),
		deployment("rondo-probe", "reg:5000/rondo-toolbox:v1.2.3", 1, 1, 0),
		deployment("rondo-gitd", "reg/rondo-toolbox", 1, 0, 1),
		servicePodObj("rondo-controller-abc", map[string]string{"app.kubernetes.io/name": "rondo-controller"}, true, 0, ""),
		servicePodObj("rondo-probe-xyz", map[string]string{"app.kubernetes.io/name": "rondo-probe"}, false, 7, "CrashLoopBackOff"),
		servicePodObj("helm-apply-1-pod", map[string]string{"job-name": "helm-apply-1"}, false, 0, ""),
	}
}

func TestServicesReportStateVersionAndPods(t *testing.T) {
	got := decode[[]ServiceView](t, get(t, newServer(t, servicesFixture()...), "/api/services"))
	if len(got) != 3 {
		t.Fatalf("%d services, want 3: %+v", len(got), got)
	}
	byName := map[string]ServiceView{}
	for _, s := range got {
		byName[s.Name] = s
	}

	ctrl := byName["rondo-controller"]
	if ctrl.State != "Healthy" || ctrl.Version != "edge-main-9c481a63" || len(ctrl.Pods) != 1 || !ctrl.Pods[0].Ready {
		t.Errorf("rondo-controller: %+v", ctrl)
	}
	probe := byName["rondo-probe"]
	if probe.State != "Down" || probe.Version != "v1.2.3" || probe.Message == "" {
		t.Errorf("rondo-probe: %+v", probe)
	}
	if p := probe.Pods; len(p) != 1 || p[0].Restarts != 7 || p[0].Reason != "CrashLoopBackOff" || p[0].Ready {
		t.Errorf("rondo-probe pods: %+v", p)
	}
	gitd := byName["rondo-gitd"]
	if gitd.State != "Progressing" || gitd.Version != "" || gitd.Pods == nil {
		t.Errorf("rondo-gitd: %+v", gitd)
	}
}

func TestServiceDegradedWhenSomeReplicasAreMissing(t *testing.T) {
	d := deployment("rondo-source", "reg/rondo-toolbox:v1", 2, 2, 1)
	if state, _ := serviceState(d, 2); state != "Degraded" {
		t.Errorf("state = %s, want Degraded", state)
	}
}

func TestLogsServeAServicePodOnly(t *testing.T) {
	srv := newServer(t, servicesFixture()...)
	if rec := get(t, srv, "/api/logs?pod=rondo-probe-xyz"); rec.Code != http.StatusOK || rec.Body.String() != "log line" {
		t.Errorf("service pod: %d %q", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"helm-apply-1-pod", "absent"} {
		if rec := get(t, srv, "/api/logs?pod="+name); rec.Code != http.StatusNotFound {
			t.Errorf("pod %s: code %d, want 404", name, rec.Code)
		}
	}
	if rec := get(t, srv, "/api/logs"); rec.Code != http.StatusBadRequest {
		t.Errorf("without a parameter: code %d, want 400", rec.Code)
	}
}
