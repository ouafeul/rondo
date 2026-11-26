// Package rondo holds the site manifest template that `rondo init` writes.
package rondo

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed rondo.yaml.example
var ExampleConfig []byte

// artifactoryArtifacts is the artifacts section of the template for the preset artifactory.
const artifactoryArtifacts = `artifacts:
  preset: artifactory
  username: svc-rondo
  caCertificateFile: ./certs/mirror-ca.pem
  releaseChannel: stable
  artifactory:
    url: https://artifactory.example.local/artifactory
    repositories:
      docker: rondo-docker-local
      helm: rondo-helm-local
      # The files, and the OpenTofu providers as a static network mirror tree under providers/.
      generic: rondo-generic-local
      rpm: ""
      tofuState: rondo-tofu-state-local
    # Lifetime of the minted provider mirror token; secrets.mirrorToken, when set, is used instead.
    mirrorTokenLifetimeSeconds: ""
  # artifacts.tofu (providerMirror or providerOCI, state) overrides the providers and the states.
`

// ExampleConfigFor is the template with the artifacts section of preset: generic (or empty) or
// artifactory.
func ExampleConfigFor(preset string) ([]byte, error) {
	switch preset {
	case "", "generic":
		return ExampleConfig, nil
	case "artifactory":
	default:
		return nil, fmt.Errorf("unknown artifact mirror preset %q: generic or artifactory", preset)
	}
	s := string(ExampleConfig)
	start := strings.Index(s, "artifacts:\n")
	end := strings.Index(s, "\ntargets:\n")
	if start < 0 || end < start {
		return nil, fmt.Errorf("the template has no artifacts section followed by targets")
	}
	return []byte(s[:start] + artifactoryArtifacts + s[end:]), nil
}
