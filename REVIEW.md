# Review rules

Report only real problems; skip style nits a linter would catch.

1. **Correctness first**: reconcile loops must be idempotent; a Job or tofu/helm step must be safe to
   re-run after an interruption; errors are returned or surfaced on the object status, never swallowed.
2. **Air gap**: nothing at runtime may reach the Internet. Every new image, chart, binary, provider or
   package must be declared in `manifests/dependencies.yaml` and fetched from the configured mirror.
3. **API**: changes under `api/v1alpha1` need regenerated CRDs (`scripts/ci/gen-crds.sh`) and docs; field
   removals or renames are breaking and must be called out.
4. **Secrets**: no credential in logs, events, status, ConfigMaps or CLI output; secrets flow through
   Kubernetes Secrets or files with restrictive modes.
5. **Tests**: a fix comes with a test that fails without it; controller behaviour is tested with envtest.
6. **Docs and changelog**: user-facing changes update `docs/` and add a `CHANGELOG.md` `[Unreleased]` line.
7. **Language**: everything in English.
