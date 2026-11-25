# Contributing to RONDO

Thanks for helping. Issues and pull requests are welcome; for anything large, open an issue
first so the design can be discussed.

## Prerequisites

- Go (version in `go.mod`), with `setup-envtest` for controller tests
- Python 3.12, [ShellCheck](https://www.shellcheck.net/), [Helm](https://helm.sh/) 3.17+,
  [OpenTofu](https://opentofu.org/) for the modules under `terraform/`
- Node.js 24 for `docs/` and `web/dashboard`
- Optional: Docker and k3d to run `rondo-sandbox`

## Layout

```
api/v1alpha1/             Component, Target, Enrollment types
cmd/rondo/                the CLI
cmd/rondo-controller/     the operator
cmd/rondo-toolbox/        Jobs, health probe, dashboard
cmd/rondo-sandbox/        local component sandbox
internal/                 shared code
platform/                 base Components and Enrollments
apps/                     first-party Helm charts
terraform/                k3s (management), rke2 (downstreams), vm (component machines)
ansible/                  component machine installation
helmfile.yaml.gotmpl      management cluster charts
manifests/                generated CRDs, third-party dependencies to mirror
scripts/ci/               CI helpers
docs/                     documentation site (Astro Starlight)
web/dashboard/            dashboard frontend
```

## Workflow

1. Branch from `main`: `feat/<slug>`, `fix/<slug>`, `docs/<slug>`, `ci/<slug>`, `chore/<slug>`.
2. Make the change with its tests; a bug fix comes with a test that fails without it.
3. Run the checks below, add a line under `## [Unreleased]` in `CHANGELOG.md` for user-facing changes.
4. Open a pull request. The **CI OK** check must pass before merge (squash merge).

## Checks

```bash
go vet ./...
export KUBEBUILDER_ASSETS="$($(go env GOPATH)/bin/setup-envtest use "$(scripts/ci/envtest-version.sh)" -p path)"
go test ./...
scripts/ci/gen-crds.sh --check                 # regenerate with scripts/ci/gen-crds.sh
go run ./cmd/rondo component lint platform
python3 -m pytest scripts/ci/tests -q && ruff check scripts/ci
shellcheck scripts/ci/*.sh install.sh
(cd docs && npm ci && npm run build)
```

Git hooks in `.githooks/` run the fast checks: `git config core.hooksPath .githooks`.

## Conventions

### Commits and pull request titles

`:emoji: type: short description`, in English, under 80 characters:

- `:sparkles: feat:` new feature
- `:bug: fix:` bug fix
- `:recycle: refactor:` no behaviour change
- `:wrench: chore:` tooling, configuration, dependencies
- `:memo: docs:` documentation
- `:white_check_mark: test:` tests only
- `:lock: security:` security change

### Shell

`#!/usr/bin/env bash`, `set -euo pipefail`, quoted `"${variables}"`, ShellCheck clean
(`# shellcheck disable=SCxxxx` only with a reason).

### Python

Python 3.12, minimal dependencies (`pyyaml`, `requests`, `certifi`), tests under `scripts/ci/tests`, `ruff` clean.

### YAML and OpenTofu

Two-space indentation, no trailing whitespace, `tofu fmt`.

## Dependencies

Every third-party artefact that must cross the air gap (binaries, Helm charts, OpenTofu
providers, images) is declared in `manifests/dependencies.yaml`. Images are not declared by
hand: `scripts/ci/extract-helm-images.py` extracts them from the rendered charts.

To add a Helm chart:

1. add it under `helm_charts` in `manifests/dependencies.yaml`;
2. add its release to `helmfile.yaml.gotmpl` or a Component under `platform/`;
3. CI renders it, extracts its images and checks the health model covers its kinds.

## Releases

See [the release guide](docs/src/content/docs/contributing/release.mdx): update `CHANGELOG.md`,
tag `vX.Y.Z` on `main`, and the workflows publish binaries, packages, images, charts and docs.
