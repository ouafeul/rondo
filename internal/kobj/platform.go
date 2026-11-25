package kobj

// PlatformInstall is what the operator manifests are rendered from.
type PlatformInstall struct {
	ControllerImage   string
	ToolboxImage      string
	TofuStateBaseURL  string
	VMModuleURL       string
	AnsibleContentURL string
	ArtifactoryURL    string

	DashboardHostname string
}

func (p PlatformInstall) Objects() []Object {
	ns := PlatformNamespace
	objs := []Object{
		namespaceObj(ns),
		serviceAccount(ControllerName, ns),
		serviceAccount(ExecServiceAccount, ns),
		controllerRole(ns),
		roleBinding(ControllerName, ns, "Role", ControllerName, ControllerName),
		clusterRoleBinding("rondo-exec-cluster-admin", ns, "cluster-admin", ExecServiceAccount),
		serviceAccount(ProbeName, ns),
		probeRole(ns),
		roleBinding(ProbeName, ns, "Role", ProbeName, ProbeName),
		clusterRoleBinding("rondo-probe-view", ns, "view", ProbeName),
		probeRBACClusterRole(),
		clusterRoleBinding(ProbeName+"-rbac-read", ns, ProbeName, ProbeName),
		p.controllerDeployment(ns),
		p.probeDeployment(ns),
	}
	objs = append(objs, p.dashboardObjects(ns)...)
	objs = append(objs, networkPolicies(ns)...)
	return objs
}

func (p PlatformInstall) dashboardObjects(ns string) []Object {
	objs := []Object{
		serviceAccount(DashboardName, ns),
		dashboardClusterRole(),
		roleBinding(DashboardName, ns, "ClusterRole", DashboardName, DashboardName),
		p.dashboardDeployment(ns),
		clusterIPService(DashboardName, ns, DashboardPort),
	}
	if p.DashboardHostname != "" {
		objs = append(objs, ingress(DashboardName, ns, p.DashboardHostname))
	}
	return objs
}

func namespaceObj(name string) Object {
	return Object{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": Object{"name": name},
	}
}

func serviceAccount(name, ns string) Object {
	return Object{
		"apiVersion": "v1", "kind": "ServiceAccount",
		"metadata": Object{"name": name, "namespace": ns},
	}
}
