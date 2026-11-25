// Package kobj names and builds the Kubernetes objects rondo writes itself, as plain maps.
// It imports the standard library only, so cmd/rondo does not pull apimachinery in through it.
package kobj

// Object is an unstructured manifest; it is identical to synth.Object.
type Object = map[string]any

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
