package kobj

// Target is the Target CR rondo.yaml projects; spec carries its backend, roles and values are added here.
func Target(name string, roles []string, backend Object, values map[string]string) Object {
	spec := Object{"values": toAnyMap(values)}
	for k, v := range backend {
		spec[k] = v
	}
	if len(roles) > 0 {
		spec["roles"] = toAny(roles)
	}
	return Object{
		"apiVersion": APIVersion,
		"kind":       "Target",
		"metadata":   Object{"name": name, "namespace": PlatformNamespace},
		"spec":       spec,
	}
}

func InClusterBackend() Object {
	return Object{"kubernetes": Object{"inCluster": true}}
}

func KubeconfigBackend(server, secretName string) Object {
	return Object{"kubernetes": Object{
		"server":              server,
		"kubeconfigSecretRef": Object{"name": secretName, "key": KubeconfigKey},
	}}
}

func VSphereBackend(objectName string) Object {
	return Object{"vsphere": Object{
		"credentialsSecretRef": Object{"name": objectName},
		"settingsConfigMapRef": Object{"name": objectName},
	}}
}
