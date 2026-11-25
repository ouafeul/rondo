---
name: steward
description: Repository rules for watching a RONDO pull request (used by /ship and /steward watches) - which checks matter, how to fix the usual CI failures, what never to touch.
---

# Watching a RONDO pull request

## Checks

- The only required check is **CI OK** (`.github/workflows/ci.yml`). Open the failing job behind it:
  `gh run view <run-id> --log-failed`.
- **Images** and **Claude review** are informative; a Trivy finding on a base image is not a reason to
  change unrelated code.

## Usual failures and fixes

- `Go (vet, test, vulncheck, build)`: reproduce locally with envtest:
  `export KUBEBUILDER_ASSETS="$($(go env GOPATH)/bin/setup-envtest use "$(scripts/ci/envtest-version.sh)" -p path)"`
  then `go test ./<pkg>/... -run <Test>`. Coverage floors live in `scripts/ci/check-coverage.py`: add
  tests, never lower a floor.
- `Manifests, CRDs, charts, OpenTofu`: `scripts/ci/gen-crds.sh` then commit `manifests/crds`; chart
  version bump in `apps/<chart>/Chart.yaml` and wherever it is pinned under `platform/`.
- `CI scripts, shell, Ansible`: `ruff check --fix scripts/ci`, `shellcheck`, `ansible-lint ansible/`.
- `Docs build` / `Dashboard build`: `npm ci && npm run build` in `docs/` or `web/dashboard`.
- `govulncheck`: bump the vulnerable module in `go.mod` (`go get <module>@<fixed>` then `go mod tidy`).

## Never

- Merge, approve, force-push, retag, or edit `CHANGELOG.md` sections of released versions.
- Silence a test, skip a check, or lower a coverage floor to get green.
- Change `.github/workflows/` to make a failing job pass unless the workflow itself is the bug.
