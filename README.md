# RONDO

**Remote & Offline Node Deployment Orchestrator** — deploy and operate components on
air-gapped Kubernetes clusters and vSphere environments.

[![CI](https://github.com/ouafeul/rondo/actions/workflows/ci.yml/badge.svg)](https://github.com/ouafeul/rondo/actions/workflows/ci.yml)
[![Images](https://github.com/ouafeul/rondo/actions/workflows/images.yml/badge.svg)](https://github.com/ouafeul/rondo/actions/workflows/images.yml)
[![Docs](https://img.shields.io/badge/docs-ouafeul.github.io%2Frondo-blue)](https://ouafeul.github.io/rondo/)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

RONDO installs components (Helm charts, vSphere machines) on targets (Kubernetes clusters,
vSphere environments) without Internet access. Everything it needs comes from a mirror on the
site. Three objects:

- `Component` — **what** to install (a chart, a set of machines, or both);
- `Target` — **where** (a Kubernetes cluster or a vSphere environment, with roles);
- `Enrollment` — **this** component on **that** target.

Targets are declared in `rondo.yaml`; components publish their own Components and Enrollments.

## Quick start

```bash
curl -fsSL https://github.com/ouafeul/rondo/releases/latest/download/install.sh | sh
rondo init                         # writes rondo.yaml: artifact mirror, targets, secrets
rondo deploy                       # management cluster, vSphere Targets
rondo deploy downstreams           # downstream rke2 clusters
rondo component install <name>     # a component
kubectl -n rondo-system get targets,enrollments
```

On an air-gapped site, mirror a release first (binaries and bundles from
[GitHub Releases](https://github.com/ouafeul/rondo/releases), images from `ghcr.io/ouafeul/rondo-*`,
charts from `oci://ghcr.io/ouafeul/charts`) and point `install.sh` at it with `RONDO_MIRROR_URL`.

To build a component locally before shipping it, use `rondo-sandbox`: the same CLI code on
local k3d clusters.

## Documentation

**<https://ouafeul.github.io/rondo/>** — getting started, concepts, operations, component
authoring and references (CLI, `rondo.yaml`, API, Jobs). Sources live in
[`docs/`](docs/src/content/docs/).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
