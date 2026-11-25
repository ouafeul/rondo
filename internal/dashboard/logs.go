package dashboard

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// pods/log is out of reach of the controller-runtime client.
type PodLogs struct {
	Clientset kubernetes.Interface
	Namespace string
}

const jobNameLabel = "job-name"

func (p *PodLogs) JobLogs(ctx context.Context, job string, tail int64) (io.ReadCloser, error) {
	pods, err := p.Clientset.CoreV1().Pods(p.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobNameLabel + "=" + job,
	})
	if err != nil {
		return nil, fmt.Errorf("pods of Job %s: %w", job, err)
	}
	pod := newestPod(pods.Items)
	if pod == nil {
		return nil, fmt.Errorf("no pod for Job %s (TTL expired?)", job)
	}
	return p.Clientset.CoreV1().Pods(p.Namespace).
		GetLogs(pod.Name, &corev1.PodLogOptions{TailLines: &tail}).Stream(ctx)
}

func (p *PodLogs) ServiceLogs(ctx context.Context, pod string, tail int64) (io.ReadCloser, error) {
	return p.Clientset.CoreV1().Pods(p.Namespace).
		GetLogs(pod, &corev1.PodLogOptions{TailLines: &tail}).Stream(ctx)
}

func newestPod(pods []corev1.Pod) *corev1.Pod {
	var out *corev1.Pod
	for i := range pods {
		if out == nil || pods[i].CreationTimestamp.After(out.CreationTimestamp.Time) {
			out = &pods[i]
		}
	}
	return out
}
