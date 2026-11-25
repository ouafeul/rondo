package kobj

func defaultDenyIngress(ns string) Object {
	return Object{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata":   Object{"name": netpolPrefix + "default-deny-ingress", "namespace": ns},
		"spec": Object{
			"podSelector": Object{},
			"policyTypes": []any{"Ingress"},
		},
	}
}

func allowFromIngressController(name, app, ns string) Object {
	return Object{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata":   Object{"name": netpolPrefix + name, "namespace": ns},
		"spec": Object{
			"podSelector": Object{
				"matchLabels": Object{LabelName: app},
			},
			"policyTypes": []any{"Ingress"},
			"ingress": []any{Object{
				"from": []any{Object{
					"namespaceSelector": Object{
						"matchLabels": Object{
							"kubernetes.io/metadata.name": IngressControllerNamespace,
						},
					},
				}},
				"ports": []any{Object{"protocol": "TCP", "port": "http"}},
			}},
		},
	}
}

func networkPolicies(ns string) []Object {
	return []Object{
		defaultDenyIngress(ns),
		allowFromIngressController("dashboard-from-ingress", DashboardName, ns),
	}
}
