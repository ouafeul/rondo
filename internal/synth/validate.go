package synth

import (
	"regexp"

	"github.com/go-playground/validator/v10"
)

var validate = newValidator()

func Validator() *validator.Validate { return validate }

var (
	kebabRe  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	semverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+].*)?$`)
)

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	_ = v.RegisterValidation("kebab-case", func(fl validator.FieldLevel) bool {
		return kebabRe.MatchString(fl.Field().String())
	})
	_ = v.RegisterValidation("semver", func(fl validator.FieldLevel) bool {
		return semverRe.MatchString(fl.Field().String())
	})
	return v
}
