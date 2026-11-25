# RONDO — notes for Claude

RONDO (Remote & Offline Node Deployment Orchestrator) installs components (Helm charts, vSphere
machines) on targets (Kubernetes clusters, vSphere environments) without Internet access. Three
objects: `Component` (what), `Target` (where), `Enrollment` (this component on this target), API group
`rondo.ouafeul.github.io/v1alpha1`.

## Layout

- `cmd/rondo` CLI, `cmd/rondo-sandbox` local sandbox, `cmd/rondo-controller` controller.
- `api/v1alpha1` CRD types → `manifests/crds` (generated, never edit by hand).
- `internal/` everything else: `controllers`, `steps` (CLI deploy steps), `exec` (Job runtimes), `tui`,
  `dashboard`, `sandbox`, `config` (`rondo.yaml`).
- `platform/` base Components and Enrollments, `apps/` first-party Helm charts, `terraform/` OpenTofu
  modules, `ansible/` VM roles, `build/` Dockerfiles, `scripts/ci/` CI helpers (Python, shell),
  `docs/` Astro Starlight site, `web/dashboard` dashboard frontend.

## Commands

```bash
go vet ./...
export KUBEBUILDER_ASSETS="$($(go env GOPATH)/bin/setup-envtest use "$(scripts/ci/envtest-version.sh)" -p path)"
go test ./...                                  # controllers need envtest (above)
python3 scripts/ci/check-coverage.py cover.out # after go test -coverprofile=cover.out
scripts/ci/gen-crds.sh                          # after any api/v1alpha1 change; --check in CI
go run ./cmd/rondo component lint platform     # after platform/ or apps/ changes
python3 -m pytest scripts/ci/tests -q && ruff check scripts/ci
shellcheck scripts/ci/*.sh cmd/rondo-sandbox/rondo-sandbox.sh install.sh
(cd docs && npm ci && npm run build)
(cd web/dashboard && npm ci && npm run lint && npm run build)
```

## Conventions

- Everything in English: code, comments, messages, docs, commits, pull requests.
- Branches `feat/<slug>`, `fix/<slug>`, `docs/<slug>`, `ci/<slug>`, `chore/<slug>` from `main`.
- Commit and PR titles: gitmoji + conventional commit, under 80 chars, e.g. `:bug: fix: enrollment waits for its target`.
- Every user-facing change adds a line under `## [Unreleased]` in `CHANGELOG.md` (Keep a Changelog).
- A bug fix comes with a test that fails without the fix.
- Chart changes under `apps/` bump the chart version; new third-party artefacts go in `manifests/dependencies.yaml`.
- Never commit secrets, site values, kubeconfigs or state files.
- Describe behaviour as it is; no project-history narration in code comments or docs.
- Never merge a pull request, never force-push `main`, never create or move release tags unless asked.

## CI (GitHub Actions)

- `ci.yml`: the single required check is **CI OK**; it fails if any job failed (skipped jobs are fine).
- `images.yml`: images to `ghcr.io/ouafeul/rondo-*` on `main` (`edge-main`, `edge-main-<sha>`, `sha-<sha>`) and on tags.
- `release.yml`: on `v*` tags; the release body is the matching `## [x.y.z]` CHANGELOG section, which must exist.
- `pages.yml`: docs to https://ouafeul.github.io/rondo/.

## Releasing

1. Move `[Unreleased]` entries under `## [x.y.z(-rc.n)] - YYYY-MM-DD` in `CHANGELOG.md`, merge that PR.
2. Tag `vX.Y.Z(-rc.N)` on `main` and push the tag; `release.yml` and `images.yml` do the rest.
