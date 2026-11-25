package kobj

func rule(group string, resources []string, verbs ...string) Object {
	return Object{
		"apiGroups": []any{group},
		"resources": toAny(resources),
		"verbs":     toAny(verbs),
	}
}

func read(group string, resources ...string) Object {
	return rule(group, resources, "get", "list", "watch")
}

func roleBinding(name, ns, roleKind, roleName, sa string) Object {
	return Object{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "RoleBinding",
		"metadata":   Object{"name": name, "namespace": ns},
		"roleRef": Object{
			"apiGroup": "rbac.authorization.k8s.io", "kind": roleKind, "name": roleName,
		},
		"subjects": []any{Object{
			"kind": "ServiceAccount", "name": sa, "namespace": ns,
		}},
	}
}

// clusterRoleBinding is a roleBinding to a ClusterRole, cluster-wide: it has no namespace.
func clusterRoleBinding(name, ns, clusterRole, sa string) Object {
	b := roleBinding(name, ns, "ClusterRole", clusterRole, sa)
	b["kind"] = "ClusterRoleBinding"
	b["metadata"] = Object{"name": name}
	return b
}

func controllerRole(ns string) Object {
	return Object{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "Role",
		"metadata":   Object{"name": ControllerName, "namespace": ns},
		"rules": []any{
			read(APIGroup, "targets", "components"),
			rule(APIGroup, []string{"enrollments"}, "get", "list", "watch", "update", "patch"),
			rule(APIGroup, []string{"targets/status", "enrollments/status"}, "get", "update", "patch"),
			rule(APIGroup, []string{"enrollments/finalizers"}, "update"),
			rule("batch", []string{"jobs"}, "get", "list", "watch", "create", "delete"),
			rule("coordination.k8s.io", []string{"leases"}, "get", "list", "watch", "create", "update", "delete"),
			read("", "secrets", "configmaps"),
			rule("", []string{"events"}, "create", "patch"),
		},
	}
}

func probeRole(ns string) Object {
	return Object{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "Role",
		"metadata":   Object{"name": ProbeName, "namespace": ns},
		"rules": []any{
			read(APIGroup, "enrollments", "targets", "components"),
			rule(APIGroup, []string{"enrollments/status"}, "get", "patch"),
			read("", "secrets"),
		},
	}
}

func probeRBACClusterRole() Object {
	return Object{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   Object{"name": ProbeName},
		"rules": []any{
			read("rbac.authorization.k8s.io",
				"roles", "rolebindings", "clusterroles", "clusterrolebindings"),
			read("apiextensions.k8s.io", "customresourcedefinitions"),
			read("storage.k8s.io", "storageclasses"),
			read("scheduling.k8s.io", "priorityclasses"),
			read("admissionregistration.k8s.io",
				"validatingwebhookconfigurations", "mutatingwebhookconfigurations"),
			read("networking.k8s.io", "ingressclasses"),
		},
	}
}

// Read-only, and no rule on secrets. A ClusterRole bound by a RoleBinding: the scope stays rondo-system.
func dashboardClusterRole() Object {
	return Object{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   Object{"name": DashboardName},
		"rules": []any{
			read(APIGroup, "components", "enrollments", "targets"),
			read("batch", "jobs"),
			read("apps", "deployments"),
			read("", "pods", "pods/log"),
		},
	}
}
