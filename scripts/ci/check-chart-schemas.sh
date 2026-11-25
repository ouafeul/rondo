#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

CHART="${1:?chart expected (e.g. apps/downstream-bootstrap)}"
EXPECTED="${2:?inventory expected (e.g. 'Certificate:cert-manager.io/v1=1,')}"
shift 2

for tool in helm kubeconform; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAILED: $tool not found in PATH." >&2
    echo "        The tooling install failed WITHOUT failing the job" >&2
    echo "        (for example a download timeout). Without this tool the render" >&2
    echo "        is empty, and an empty render would pass for a healthy chart." >&2
    exit 1
  fi
done
helm version --short >/dev/null || { echo "FAILED: helm is present but does not work." >&2; exit 1; }

rendered="$(mktemp --suffix=.yaml)"  # kubeconform filters on the extension
trap 'rm -f "$rendered"' EXIT

echo "==> ${CHART}"
if ! helm template check "$CHART" "$@" > "$rendered"; then
  echo "FAILED: rendering ${CHART} failed." >&2
  exit 1
fi

actual="$(awk '
  /^apiVersion:/ { av=$2 }
  /^kind:/       { print $2 ":" av }
' "$rendered" | sort | uniq -c | awk '{printf "%s=%s\n", $2, $1}' | sort | paste -sd, -)"

want="$(printf '%s' "$EXPECTED" | tr ',' '\n' | sed '/^$/d' | sort | paste -sd, -)"

if [ -z "$actual" ]; then
  echo "FAILED: ${CHART} rendered NO resources." >&2
  echo "        Zero resources is not a valid chart: it is a broken chart, or" >&2
  echo "        values that disable everything. A gate that accepts an empty" >&2
  echo "        render checks nothing." >&2
  exit 1
fi

if [ "$actual" != "$want" ]; then
  echo "FAILED: the inventory rendered by ${CHART} does not match the expected one." >&2
  echo "  want: ${want}" >&2
  echo "  got:  ${actual}" >&2
  echo >&2
  echo "        This check protects the resources kubeconform has NO schema for:" >&2
  echo "        it skips them, so on its own it would not notice them disappear." >&2
  echo "        For example, without the IPAddressPool the cluster has no ingress" >&2
  echo "        IP, and a schema-only gate stays green." >&2
  echo "        If the change is intended, update the inventory in the CI workflow." >&2
  exit 1
fi
echo "    inventory matches: ${actual}"

summary="$(kubeconform -strict -ignore-missing-schemas -summary -schema-location default "$rendered" 2>&1 | tail -1)"
echo "    ${summary}"

valid=$(printf '%s' "$summary"   | sed -n 's/.*Valid: \([0-9]*\).*/\1/p')
invalid=$(printf '%s' "$summary" | sed -n 's/.*Invalid: \([0-9]*\).*/\1/p')
errors=$(printf '%s' "$summary"  | sed -n 's/.*Errors: \([0-9]*\).*/\1/p')
skipped=$(printf '%s' "$summary" | sed -n 's/.*Skipped: \([0-9]*\).*/\1/p')

if [ -z "$valid$invalid$errors$skipped" ]; then
  echo "FAILED: unreadable kubeconform summary; its format changed, so this" >&2
  echo "        check measures nothing. Do NOT ignore it: this is how a gate" >&2
  echo "        becomes decorative." >&2
  exit 1
fi
found=$(printf '%s' "$summary" | sed -n 's/.*Summary: \([0-9]*\) resource.*/\1/p')
if [ "${found:-0}" -eq 0 ]; then
  echo "FAILED: kubeconform examined NO resources, although the render contains" >&2
  echo "        some. Known cause: it filters by file EXTENSION and silently" >&2
  echo "        ignores a temporary file without .yaml." >&2
  echo "        Reporting \"OK\" after ignoring everything must never pass." >&2
  exit 1
fi
if [ "${invalid:-0}" -gt 0 ] || [ "${errors:-0}" -gt 0 ]; then
  echo "FAILED: ${invalid} invalid, ${errors} schema error(s)." >&2
  exit 1
fi
if [ "${skipped:-0}" -gt 0 ]; then
  echo "    NOTE: ${skipped} resource(s) skipped for lack of a schema (third-party CRs)." \
       "They are covered ONLY by the inventory check above."
fi
echo "    OK (${valid} validated by schema, ${skipped} by inventory only)"
