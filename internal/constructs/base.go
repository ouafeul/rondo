package constructs

import (
	"github.com/go-playground/validator/v10"

	"rondo/internal/synth"
)

type Object = synth.Object

var validate *validator.Validate = synth.Validator()

func stdLabels(name, componentType string) map[string]string {
	return synth.StdLabels(name, componentType)
}
