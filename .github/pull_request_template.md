<!-- Title: gitmoji + conventional commit style, English, under 80 chars (e.g. ":bug: fix: ..."). -->

## What and why

Closes #

## What changed

-

## Proof

<!-- Name the test AND the mutation that makes it fail. -->

| Test / command | Mutation | Seen failing |
|---|---|---|
| | | |

## Checklist

- [ ] `go vet ./... && go test ./...` green (with envtest)
- [ ] `scripts/ci/gen-crds.sh --check` if `api/v1alpha1/` changed
- [ ] `rondo component lint platform` if `platform/` or an `apps/*` chart changed
- [ ] Chart version bumped if an `apps/*` chart changed
- [ ] `manifests/dependencies.yaml` updated if a new artifact crosses the air gap
- [ ] `CHANGELOG.md` `[Unreleased]` entry for user-facing changes; docs updated
- [ ] No secret or site-specific value in the diff
