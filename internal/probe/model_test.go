package probe

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEveryModelledKindIsRecognisedByAssess(t *testing.T) {
	kinds := ModelledKinds()
	if len(kinds) == 0 {
		t.Fatal("ModelledKinds is empty: the CI guard would guard nothing")
	}

	for _, k := range kinds {
		t.Run(k, func(t *testing.T) {
			group, kind, ok := strings.Cut(k, "/")
			if !ok {
				t.Fatalf("malformed key %q: want \"<group>/<Kind>\"", k)
			}
			apiVersion := "v1"
			if group != "" {
				apiVersion = group + "/v1"
			}

			o := &unstructured.Unstructured{}
			o.SetAPIVersion(apiVersion)
			o.SetKind(kind)
			o.SetName("an-object")

			got := Assess(o)
			if strings.Contains(got.Message, "not modelled") {
				t.Errorf("%s is listed as modelled by ModelledKinds but Assess answers %q: "+
					"the CI guard would think it covered while the Release would stay "+
					"Unknown and block everything downstream of it", k, got.Message)
			}
		})
	}
}

func TestKindOutsideTheTableIsReportedAsNotModelled(t *testing.T) {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion("acme.example.com/v1")
	o.SetKind("CoffeeMachine")
	o.SetName("an-object")

	got := Assess(o)
	if !strings.Contains(got.Message, "not modelled") {
		t.Errorf("an unknown kind without a Ready condition returns %q (%s): the CI guard "+
			"would never see a missing GVK", got.Message, got.Health)
	}
}
