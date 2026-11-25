package synth

import "rondo/internal/version"

const (
	labelManagedBy     = "app.kubernetes.io/managed-by"
	labelComponentName = "rondo.ouafeul.github.io/component-name"
	labelComponentType = "rondo.ouafeul.github.io/component-type"
	labelSDKVersion    = "rondo.ouafeul.github.io/synth-version"

	managedByValue = "rondo-synth"
)

func StdLabels(name, componentType string) map[string]string {
	return map[string]string{
		labelManagedBy:     managedByValue,
		labelComponentName: name,
		labelComponentType: componentType,
		labelSDKVersion:    version.Version,
	}
}
