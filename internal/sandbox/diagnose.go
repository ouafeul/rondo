package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rondo/api/v1alpha1"
	"rondo/internal/kobj"
	"rondo/internal/log"
)

// Deadlocks the Enrollments' status does not tell: they wait, RONDO cannot.

// operatorCrash names an operator pod in CrashLoopBackOff: no Enrollment can conclude then.
func (sb *sandbox) operatorCrash(ctx context.Context) error {
	pods, err := sb.pods(ctx, sb.ws.Kubeconfig(), kobj.PlatformNamespace)
	if err != nil {
		return nil
	}
	for _, p := range pods {
		if p.Metadata.Labels["app.kubernetes.io/name"] == "" || !p.crashing() {
			continue
		}
		name := p.Metadata.Labels["app.kubernetes.io/name"]
		logs, _ := sb.kube.Output(ctx, sb.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "logs",
			p.Metadata.Name, "--previous", "--tail=25")
		fmt.Fprintf(os.Stderr, "\n── %s, before its last restart ──\n%s\n", p.Metadata.Name, logs)
		return fmt.Errorf("%s in CrashLoopBackOff: an RONDO defect, not the component's; as long as it "+
			"restarts, no Enrollment reaches a verdict", name)
	}
	return nil
}

// adoptedJob finds an Enrollment whose Job belongs to another one: two Job names collided
// (internal/jobs Name), and its workload was never applied on its own Target.
func (sb *sandbox) adoptedJob(ctx context.Context, list []v1alpha1.Enrollment, watched map[string]bool) error {
	out, err := sb.kube.Output(ctx, sb.ws.Kubeconfig(), "-n", kobj.PlatformNamespace, "get", "jobs",
		"-o", "json")
	if err != nil {
		return nil
	}
	var jobs struct {
		Items []struct {
			Metadata struct {
				Name            string `json:"name"`
				OwnerReferences []struct {
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(out), &jobs) != nil {
		return nil
	}
	owner := map[string]string{}
	for _, j := range jobs.Items {
		if len(j.Metadata.OwnerReferences) > 0 {
			owner[j.Metadata.Name] = j.Metadata.OwnerReferences[0].Name
		}
	}
	for _, e := range list {
		if !watched[e.Name] || e.Status.LastAttempt == nil {
			continue
		}
		job := e.Status.LastAttempt.JobName
		if o, ok := owner[job]; ok && o != e.Name {
			return fmt.Errorf("Enrollment %s took over Job %s of Enrollment %s: their Job names "+
				"collide (truncated name prefix, same hash), its workload was never applied "+
				"on %s; an RONDO defect (internal/jobs, Name)", e.Name, job, o, e.Status.ResolvedTarget)
		}
	}
	return nil
}

// nodePressure names a node under disk, memory or PID pressure: its kubelet evicts pods and
// places no new one, and it does not recover by waiting. Disk pressure is the workstation's:
// the k3d nodes keep their images on Docker's filesystem.
func (sb *sandbox) nodePressure(ctx context.Context) error {
	for _, c := range sb.site.Clusters {
		out, err := sb.kube.Output(ctx, filepath.Join(sb.s.clusterDir(c.Name), "kubeconfig.yaml"),
			"get", "nodes", "-o", "json")
		if err != nil {
			continue
		}
		var nodes struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Conditions []struct {
						Type, Status, Message string
					} `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(out), &nodes) != nil {
			continue
		}
		for _, n := range nodes.Items {
			for _, cond := range n.Status.Conditions {
				if cond.Status != "True" || !strings.HasSuffix(cond.Type, "Pressure") {
					continue
				}
				hint := ""
				if cond.Type == "DiskPressure" {
					hint = "; the workstation's Docker disk is almost full: docker builder prune, " +
						"docker image prune, rondo-sandbox destroy --purge"
				}
				return fmt.Errorf("node %s (cluster %s) is under %s, its kubelet evicts pods: %s%s",
					n.Metadata.Name, c.Name, cond.Type, cond.Message, hint)
			}
		}
	}
	return nil
}

type pod struct {
	Metadata struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		Labels          map[string]string `json:"labels"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Ready        bool  `json:"ready"`
			RestartCount int32 `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"waiting"`
			} `json:"state"`
		} `json:"containerStatuses"`
		InitContainerStatuses []struct {
			State struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
			} `json:"state"`
		} `json:"initContainerStatuses"`
	} `json:"status"`
}

// crashing says a pod's container keeps restarting: between two back-offs it runs, briefly.
func (p pod) crashing() bool {
	for _, c := range p.Status.ContainerStatuses {
		if c.RestartCount >= 3 && !c.Ready {
			return true
		}
	}
	return p.waiting() == "CrashLoopBackOff"
}

// waiting is why a pod is not running, "" when it is (or is done).
func (p pod) waiting() string {
	for _, c := range p.Status.InitContainerStatuses {
		if c.State.Waiting != nil && c.State.Waiting.Reason != "PodInitializing" {
			return "init:" + c.State.Waiting.Reason
		}
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.State.Waiting != nil {
			return c.State.Waiting.Reason
		}
	}
	switch p.Status.Phase {
	case "Pending":
		return "Pending"
	case "Running":
		for _, c := range p.Status.ContainerStatuses {
			if !c.Ready {
				return "NotReady"
			}
		}
	}
	return ""
}

func (sb *sandbox) pods(ctx context.Context, kubeconfig, ns string) ([]pod, error) {
	args := []string{"get", "pods", "-o", "json"}
	if ns == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "-n", ns)
	}
	out, err := sb.kube.Output(ctx, kubeconfig, args...)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []pod `json:"items"`
	}
	return list.Items, json.Unmarshal([]byte(out), &list)
}

// stuckPods says, cluster by cluster, which pods do not run and the last warning about each.
func (sb *sandbox) stuckPods(ctx context.Context) {
	log.Warn("no change for a while: the pods that are not running")
	any := false
	for _, c := range sb.site.Clusters {
		kc := filepath.Join(sb.s.clusterDir(c.Name), "kubeconfig.yaml")
		pods, err := sb.pods(ctx, kc, "")
		if err != nil {
			continue
		}
		warnings := sb.warnings(ctx, kc)
		for _, p := range pods {
			if p.Metadata.Namespace == "kube-system" {
				continue
			}
			reason := p.waiting()
			if reason == "" {
				continue
			}
			any = true
			key := p.Metadata.Namespace + "/" + p.Metadata.Name
			log.Warn(key, "cluster", c.Name, "state", reason, "last event", truncate(warnings[key], 200))
		}
	}
	if !any {
		log.Info("all pods are running: RONDO is waiting for a dependency or the probe")
	}
}

func (sb *sandbox) warnings(ctx context.Context, kubeconfig string) map[string]string {
	out, err := sb.kube.Output(ctx, kubeconfig, "get", "events", "-A", "--field-selector", "type=Warning",
		"-o", "json")
	if err != nil {
		return nil
	}
	var list struct {
		Items []struct {
			LastTimestamp  string `json:"lastTimestamp"`
			Message        string `json:"message"`
			InvolvedObject struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"involvedObject"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(out), &list) != nil {
		return nil
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].LastTimestamp < list.Items[j].LastTimestamp })
	last := map[string]string{}
	for _, e := range list.Items {
		last[e.InvolvedObject.Namespace+"/"+e.InvolvedObject.Name] = strings.TrimSpace(e.Message)
	}
	return last
}
