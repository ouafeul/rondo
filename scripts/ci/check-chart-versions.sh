#!/usr/bin/env bash
set -euo pipefail

DIFF=(git diff)
BASE="${1:-${CHART_DIFF_BASE:-HEAD~1}}"
if [ "${1:-}" = "--staged" ]; then
  DIFF=(git diff --cached)
  BASE="HEAD"
fi
echo "==> Chart version guard (base = ${BASE})"
if ! git rev-parse --verify --quiet "${BASE}^{commit}" >/dev/null; then
  echo "    no base commit to compare with (first commit or new branch), nothing to check"
  exit 0
fi
fail=0

for chart in apps/*/; do
  [ -f "${chart}Chart.yaml" ] || continue
  name="$(basename "$chart")"

  if "${DIFF[@]}" --quiet "${BASE}" -- "${chart}templates" "${chart}values.yaml" 2>/dev/null; then
    continue
  fi

  if "${DIFF[@]}" "${BASE}" -- "${chart}Chart.yaml" 2>/dev/null | grep -qE '^\+version:'; then
    echo "  OK   ${name}, content changed + version bumped"
  else
    echo "  FAIL ${name}, templates/values changed but Chart.yaml 'version' NOT bumped"
    fail=1
  fi
done

for vfile in terraform/*/VERSION; do
  [ -f "$vfile" ] || continue
  mod="$(dirname "$vfile")"
  name="$(basename "$mod")"

  if "${DIFF[@]}" --quiet "${BASE}" -- "$mod" ":(exclude)$vfile" 2>/dev/null; then
    continue
  fi

  if "${DIFF[@]}" "${BASE}" -- "$vfile" 2>/dev/null | grep -qE '^\+[0-9]+\.[0-9]+\.[0-9]+'; then
    echo "  OK   ${name} (tofu module), content changed + VERSION bumped"
  else
    echo "  FAIL ${name} (tofu module), content changed but ${vfile} NOT bumped"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "FAILED: bump the version of the artifacts above; otherwise the same published path" >&2
  echo "        carries different content, and the Components pinning it receive" >&2
  echo "        something other than what they validated." >&2
  exit 1
fi
echo "==> OK: every artifact with changed content has its version bumped."
