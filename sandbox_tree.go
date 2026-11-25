package rondo

import "embed"

// SandboxTree is what rondo-sandbox deploys RONDO from without a clone: this version's CRDs,
// RONDO tree, first-party charts and dependencies manifest, and for machine components RONDO's
// playbook and the interface of its machine module. Its images come from the registry RONDO publishes them to.
//
//go:embed manifests/crds manifests/dependencies.yaml platform apps ansible terraform/vm
var SandboxTree embed.FS
