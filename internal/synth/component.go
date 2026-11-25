package synth

type Object = map[string]any

type Component interface {
	ComponentName() string
	Validate() error
	Manifests() ([]Object, error)
}
