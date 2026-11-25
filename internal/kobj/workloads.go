package kobj

func podSecurityContext() Object {
	return Object{
		"runAsNonRoot":   true,
		"runAsUser":      int64(nonRootUID),
		"runAsGroup":     int64(nonRootUID),
		"seccompProfile": Object{"type": "RuntimeDefault"},
	}
}

func containerSecurityContext() Object {
	return Object{
		"allowPrivilegeEscalation": false,
		"readOnlyRootFilesystem":   true,
		"capabilities":             Object{"drop": []any{"ALL"}},
	}
}

func resources(cpuReq, memReq, cpuLim, memLim string) Object {
	return Object{
		"requests": Object{"cpu": cpuReq, "memory": memReq},
		"limits":   Object{"cpu": cpuLim, "memory": memLim},
	}
}

func probeObj(path string, port, delay, period int) Object {
	return Object{
		"httpGet":             Object{"path": path, "port": port},
		"initialDelaySeconds": delay,
		"periodSeconds":       period,
	}
}

// deployment wraps one hardened container in a single-replica Deployment named after its ServiceAccount.
func deployment(name, ns string, recreate bool, container Object) Object {
	labels := Object{LabelName: name}
	spec := Object{
		"replicas": 1,
		"selector": Object{"matchLabels": labels},
		"template": Object{
			"metadata": Object{"labels": labels},
			"spec": Object{
				"serviceAccountName": name,
				"securityContext":    podSecurityContext(),
				"containers":         []any{container},
			},
		},
	}
	if recreate {
		spec["strategy"] = Object{"type": "Recreate"}
	}
	return Object{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   Object{"name": name, "namespace": ns, "labels": labels},
		"spec":       spec,
	}
}

func envVar(name, value string) Object { return Object{"name": name, "value": value} }

func (p PlatformInstall) controllerDeployment(ns string) Object {
	return deployment(ControllerName, ns, true, Object{
		"name":  "controller",
		"image": p.ControllerImage,
		"args":  []any{"--health-probe-bind-address=:8081"},
		"env": []any{
			Object{"name": EnvPodName, "valueFrom": Object{
				"fieldRef": Object{"fieldPath": "metadata.name"},
			}},
			envVar(EnvNamespace, ns),
			envVar(EnvToolboxImage, p.ToolboxImage),
			envVar(EnvExecSA, ExecServiceAccount),
			envVar(EnvCACM, MirrorCACM),
			envVar(EnvCredsSecret, CredentialsSecret),
			envVar(EnvTofuStateURL, p.TofuStateBaseURL),
			envVar(EnvTofuStateS3, p.TofuStateS3),
			envVar(EnvTofuRCSecret, TofurcSecret),
			envVar(EnvVMModuleURL, p.VMModuleURL),
			envVar(EnvAnsibleContentURL, p.AnsibleContentURL),
			envVar(EnvVMSSHSecret, VMSSHSecret),
			envVar(EnvMirrorFilesURL, p.MirrorFilesURL),
		},
		"livenessProbe":   probeObj("/healthz", 8081, 15, 20),
		"readinessProbe":  probeObj("/readyz", 8081, 5, 10),
		"resources":       resources("50m", "192Mi", "500m", "1Gi"),
		"securityContext": containerSecurityContext(),
	})
}

func (p PlatformInstall) probeDeployment(ns string) Object {
	return deployment(ProbeName, ns, true, Object{
		"name":            "probe",
		"image":           p.ToolboxImage,
		"args":            []any{"probe"},
		"env":             []any{envVar(EnvNamespace, ns)},
		"resources":       resources("25m", "64Mi", "200m", "256Mi"),
		"securityContext": containerSecurityContext(),
	})
}

func (p PlatformInstall) dashboardDeployment(ns string) Object {
	return deployment(DashboardName, ns, false, Object{
		"name":            "dashboard",
		"image":           p.ToolboxImage,
		"args":            []any{"dashboard"},
		"env":             []any{envVar(EnvNamespace, ns)},
		"ports":           []any{Object{"name": "http", "containerPort": 8080}},
		"livenessProbe":   probeObj("/healthz", 8080, 10, 20),
		"readinessProbe":  probeObj("/healthz", 8080, 3, 10),
		"resources":       resources("10m", "32Mi", "200m", "128Mi"),
		"securityContext": containerSecurityContext(),
	})
}

func clusterIPService(name, ns string, port int) Object {
	return Object{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   Object{"name": name, "namespace": ns},
		"spec": Object{
			"type":     "ClusterIP",
			"selector": Object{LabelName: name},
			"ports": []any{Object{
				"name": "http", "port": port, "targetPort": "http",
			}},
		},
	}
}

func ingress(name, ns, hostname string) Object {
	return Object{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata": Object{
			"name": name, "namespace": ns,
			"annotations": Object{
				"cert-manager.io/cluster-issuer":                   "cluster-ca-issuer",
				"traefik.ingress.kubernetes.io/router.entrypoints": "websecure",
				"traefik.ingress.kubernetes.io/router.tls":         "true",
			},
		},
		"spec": Object{
			"ingressClassName": "traefik",
			"tls": []any{Object{
				"hosts": []any{hostname}, "secretName": name + "-tls",
			}},
			"rules": []any{Object{
				"host": hostname,
				"http": Object{"paths": []any{Object{
					"path":     "/",
					"pathType": "Prefix",
					"backend": Object{"service": Object{
						"name": name,
						"port": Object{"name": "http"},
					}},
				}}},
			}},
		},
	}
}
