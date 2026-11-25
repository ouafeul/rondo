#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

CONTROLLER_GEN_VERSION="v0.21.0"
CRD_DIR="manifests/crds"
API_PATHS="./api/v1alpha1/..."

bin="$(go env GOPATH)/bin/controller-gen"
if [ ! -x "$bin" ] || ! "$bin" --version 2>/dev/null | grep -q "$CONTROLLER_GEN_VERSION"; then
  echo "==> installing controller-gen $CONTROLLER_GEN_VERSION"
  go install "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}"
fi

check=false
if [ "${1:-}" = "--check" ]; then
  check=true
fi

if $check; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  "$bin" crd paths="$API_PATHS" output:crd:artifacts:config="$tmp"
  if ! diff -ru "$CRD_DIR" "$tmp" >/dev/null 2>&1; then
    echo "FAILED: $CRD_DIR is out of date with api/v1alpha1." >&2
    diff -ru "$CRD_DIR" "$tmp" >&2 || true
    echo "" >&2
    echo "Fix: run scripts/ci/gen-crds.sh, then commit $CRD_DIR." >&2
    exit 1
  fi
  gen="api/v1alpha1/zz_generated.deepcopy.go"
  if [ ! -f "$gen" ]; then
    echo "FAILED: $gen is missing. Fix: run scripts/ci/gen-crds.sh" >&2
    exit 1
  fi
  cp "$gen" "$tmp/deepcopy.before"
  "$bin" object:headerFile=/dev/null paths="$API_PATHS"
  if ! diff -u "$tmp/deepcopy.before" "$gen" >/dev/null 2>&1; then
    diff -u "$tmp/deepcopy.before" "$gen" >&2 || true
    cp "$tmp/deepcopy.before" "$gen"
    echo "" >&2
    echo "FAILED: $gen is out of date with the api/v1alpha1 types." >&2
    echo "Fix: run scripts/ci/gen-crds.sh, then commit." >&2
    exit 1
  fi
  echo "OK: CRDs and deepcopy are up to date."
  exit 0
fi

mkdir -p "$CRD_DIR"
"$bin" object:headerFile=/dev/null paths="$API_PATHS"
"$bin" crd paths="$API_PATHS" output:crd:artifacts:config="$CRD_DIR"
echo "OK: $CRD_DIR and deepcopy regenerated."
