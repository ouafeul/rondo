package probe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"rondo/api/v1alpha1"
)

type Health = string

type Assessment struct {
	GVK       string
	Namespace string
	Name      string
	Health    Health
	Message   string
}

func (a Assessment) String() string {
	id := a.GVK + " " + a.Name
	if a.Namespace != "" {
		id = a.GVK + " " + a.Namespace + "/" + a.Name
	}
	if a.Message == "" {
		return id + ": " + a.Health
	}
	return id + ": " + a.Health + ", " + a.Message
}

type conditionKind struct {
	condition string
}

var conditionTable = map[string]conditionKind{
	"cert-manager.io/Certificate":   {condition: "Ready"},
	"cert-manager.io/ClusterIssuer": {condition: "Ready"},
	"cert-manager.io/Issuer":        {condition: "Ready"},

	"rondo.ouafeul.github.io/Target":     {condition: v1alpha1.ConditionReady},
	"rondo.ouafeul.github.io/Enrollment": {condition: v1alpha1.ConditionReady},
}

var statelessKinds = map[string]bool{
	"/ConfigMap":                                                  true,
	"/Secret":                                                     true,
	"/Service":                                                    true,
	"/ServiceAccount":                                             true,
	"/Namespace":                                                  true,
	"metallb.io/IPAddressPool":                                    true,
	"metallb.io/L2Advertisement":                                  true,
	"metallb.io/BGPAdvertisement":                                 true,
	"rbac.authorization.k8s.io/Role":                              true,
	"rbac.authorization.k8s.io/RoleBinding":                       true,
	"rbac.authorization.k8s.io/ClusterRole":                       true,
	"rbac.authorization.k8s.io/ClusterRoleBinding":                true,
	"networking.k8s.io/Ingress":                                   true,
	"networking.k8s.io/NetworkPolicy":                             true,
	"networking.k8s.io/IngressClass":                              true,
	"policy/PodDisruptionBudget":                                  true,
	"scheduling.k8s.io/PriorityClass":                             true,
	"apiextensions.k8s.io/CustomResourceDefinition":               true,
	"admissionregistration.k8s.io/ValidatingWebhookConfiguration": true,
	"admissionregistration.k8s.io/MutatingWebhookConfiguration":   true,
	"traefik.io/IngressRoute":                                     true,
	"traefik.io/IngressRouteTCP":                                  true,
	"traefik.io/Middleware":                                       true,
	"traefik.io/TLSOption":                                        true,
	"traefik.io/TLSStore":                                         true,
	"traefik.io/ServersTransport":                                 true,
	"storage.k8s.io/StorageClass":                                 true,
}

func key(group, kind string) string { return group + "/" + kind }

func declaredTable(rules []v1alpha1.HealthRule) map[string]v1alpha1.HealthRule {
	if len(rules) == 0 {
		return nil
	}
	out := make(map[string]v1alpha1.HealthRule, len(rules))
	for _, r := range rules {
		out[key(r.Group, r.Kind)] = r
	}
	return out
}

func Assess(obj *unstructured.Unstructured) Assessment {
	return AssessWith(obj, nil)
}

func AssessWith(obj *unstructured.Unstructured, declared map[string]v1alpha1.HealthRule) Assessment {
	gv := obj.GroupVersionKind()
	a := Assessment{
		GVK:       gv.Kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
	k := key(gv.Group, gv.Kind)

	switch k {
	case "apps/Deployment", "apps/StatefulSet":
		a.Health, a.Message = assessReplicas(obj)
		return a
	case "apps/DaemonSet":
		a.Health, a.Message = assessDaemonSet(obj)
		return a
	case "batch/Job":
		a.Health, a.Message = assessJob(obj)
		return a
	case "/Pod":
		a.Health, a.Message = assessPod(obj)
		return a
	case "/PersistentVolumeClaim":
		a.Health, a.Message = assessPVC(obj)
		return a
	}

	if ck, ok := conditionTable[k]; ok {
		a.Health, a.Message = assessCondition(obj, ck.condition)
		return a
	}
	if statelessKinds[k] {
		a.Health = v1alpha1.HealthHealthy
		return a
	}

	if r, ok := declared[k]; ok {
		if r.Condition == "" {
			a.Health = v1alpha1.HealthHealthy
			return a
		}
		a.Health, a.Message = assessCondition(obj, r.Condition)
		return a
	}

	if h, msg := assessCondition(obj, v1alpha1.ConditionReady); h != v1alpha1.HealthUnknown {
		a.Health = h
		a.Message = strings.TrimSpace(msg + " (kind not modelled, judged on Ready)")
		return a
	}
	a.Health = v1alpha1.HealthUnknown
	a.Message = fmt.Sprintf("kind %s not modelled: declare it in the Component's spec.health, "+
		"or add an entry to internal/probe if the platform ships it", k)
	return a
}

func assessReplicas(obj *unstructured.Unstructured) (Health, string) {
	desired := nestedInt(obj, 1, "spec", "replicas")
	if desired == 0 {
		return v1alpha1.HealthHealthy, "scaled to zero"
	}
	ready := nestedInt(obj, 0, "status", "readyReplicas")
	if ready >= desired {
		// Not GetGeneration(): it returns 0 on a YAML-decoded object.
		if gen, obs := nestedInt(obj, 0, "metadata", "generation"), nestedInt(obj, 0, "status", "observedGeneration"); obs < gen {
			return v1alpha1.HealthProgressing, "rollout in progress (generation not observed)"
		}
		return v1alpha1.HealthHealthy, ""
	}
	return v1alpha1.HealthProgressing, fmt.Sprintf("%d/%d replicas ready", ready, desired)
}

func assessDaemonSet(obj *unstructured.Unstructured) (Health, string) {
	desired := nestedInt(obj, 0, "status", "desiredNumberScheduled")
	ready := nestedInt(obj, 0, "status", "numberReady")
	if desired == 0 {
		return v1alpha1.HealthProgressing, "no pod scheduled (node selector? taints?)"
	}
	if ready >= desired {
		return v1alpha1.HealthHealthy, ""
	}
	return v1alpha1.HealthProgressing, fmt.Sprintf("%d/%d pods ready", ready, desired)
}

func assessJob(obj *unstructured.Unstructured) (Health, string) {
	if st, msg := conditionStatus(obj, "Complete"); st == "True" {
		return v1alpha1.HealthHealthy, msg
	}
	if st, msg := conditionStatus(obj, "Failed"); st == "True" {
		if msg == "" {
			msg = "job failed (see the pod logs)"
		}
		return v1alpha1.HealthDegraded, msg
	}
	return v1alpha1.HealthProgressing, "job running"
}

func assessPod(obj *unstructured.Unstructured) (Health, string) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	switch phase {
	case "Running", "Succeeded":
		return v1alpha1.HealthHealthy, ""
	case "Failed":
		return v1alpha1.HealthDegraded, podFailureReason(obj)
	case "Pending":
		return v1alpha1.HealthProgressing, podFailureReason(obj)
	default:
		return v1alpha1.HealthUnknown, "phase " + phase
	}
}

func podFailureReason(obj *unstructured.Unstructured) string {
	statuses, _, _ := unstructured.NestedSlice(obj.Object, "status", "containerStatuses")
	for _, s := range statuses {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		for _, state := range []string{"waiting", "terminated"} {
			reason, found, _ := unstructured.NestedString(m, "state", state, "reason")
			if found && reason != "" {
				name, _, _ := unstructured.NestedString(m, "name")
				return name + ": " + reason
			}
		}
	}
	if msg, found, _ := unstructured.NestedString(obj.Object, "status", "message"); found && msg != "" {
		return msg
	}
	return ""
}

func assessPVC(obj *unstructured.Unstructured) (Health, string) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	switch phase {
	case "Bound":
		return v1alpha1.HealthHealthy, ""
	case "Pending":
		return v1alpha1.HealthProgressing, "volume not provisioned (storageClass missing or without a provisioner?)"
	case "Lost":
		return v1alpha1.HealthDegraded, "volume lost"
	default:
		return v1alpha1.HealthUnknown, "phase " + phase
	}
}

func assessCondition(obj *unstructured.Unstructured, name string) (Health, string) {
	st, msg := conditionStatus(obj, name)
	switch st {
	case "True":
		return v1alpha1.HealthHealthy, msg
	case "False":
		if msg == "" {
			msg = "condition " + name + " is False"
		}
		return v1alpha1.HealthDegraded, msg
	case "Unknown":
		return v1alpha1.HealthProgressing, msg
	default:
		return v1alpha1.HealthUnknown, ""
	}
}

func conditionStatus(obj *unstructured.Unstructured, name string) (status, message string) {
	conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return "", ""
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _, _ := unstructured.NestedString(m, "type")
		if t != name {
			continue
		}
		status, _, _ = unstructured.NestedString(m, "status")
		reason, _, _ := unstructured.NestedString(m, "reason")
		msg, _, _ := unstructured.NestedString(m, "message")
		return status, strings.TrimSpace(reason + " " + msg)
	}
	return "", ""
}

func nestedInt(obj *unstructured.Unstructured, def int64, fields ...string) int64 {
	raw, found, err := unstructured.NestedFieldNoCopy(obj.Object, fields...)
	if err != nil || !found || raw == nil {
		return def
	}
	switch v := raw.(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return def
		}
		return n
	default:
		return def
	}
}

func Aggregate(assessments []Assessment) (Health, string) {
	if len(assessments) == 0 {
		return v1alpha1.HealthUnknown, "no object delivered"
	}
	buckets := map[Health][]string{}
	for _, a := range assessments {
		if a.Health == v1alpha1.HealthHealthy {
			continue
		}
		buckets[a.Health] = append(buckets[a.Health], a.String())
	}
	for _, h := range []Health{v1alpha1.HealthDegraded, v1alpha1.HealthUnknown, v1alpha1.HealthProgressing} {
		if msgs := buckets[h]; len(msgs) > 0 {
			sort.Strings(msgs)
			return h, strings.Join(msgs, "; ")
		}
	}
	return v1alpha1.HealthHealthy, ""
}
