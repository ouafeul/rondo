package constructs

type MetalLBConfig struct {
	IngressIP string `validate:"required,ip"`
}

func (m *MetalLBConfig) ComponentName() string { return "metallb-config" }

func (m *MetalLBConfig) Validate() error { return validate.Struct(m) }

func (m *MetalLBConfig) Manifests() ([]Object, error) {
	labels := stdLabels(m.ComponentName(), "MetalLBConfig")
	pool := Object{
		"apiVersion": "metallb.io/v1beta1",
		"kind":       "IPAddressPool",
		"metadata":   Object{"name": "default-pool", "namespace": "metallb-system", "labels": labels},
		"spec":       Object{"addresses": []any{m.IngressIP + "/32"}},
	}
	l2 := Object{
		"apiVersion": "metallb.io/v1beta1",
		"kind":       "L2Advertisement",
		"metadata":   Object{"name": "default-l2", "namespace": "metallb-system", "labels": labels},
		"spec":       Object{"ipAddressPools": []any{"default-pool"}},
	}
	return []Object{pool, l2}, nil
}
