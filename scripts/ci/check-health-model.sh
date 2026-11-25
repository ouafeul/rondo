#!/usr/bin/env bash
set -euo pipefail

rendered=${1:-}
if [[ -z $rendered ]]; then
  echo "usage: $0 <rendered-gvks-file>" >&2
  exit 2
fi
if [[ ! -s $rendered ]]; then
  echo "==> FAILED: $rendered is empty or missing. No shape to compare, so" >&2
  echo "    no guarantee: --emit-gvks did not run, or no chart was rendered." >&2
  exit 1
fi

echo "==> Health model guard: shipped shapes ⊆ modelled shapes"

modelled=$(mktemp); trap 'rm -f "$modelled"' EXIT
go run ./internal/probe/cmd/gvkdump | sort -u > "$modelled"
if [[ ! -s $modelled ]]; then
  echo "==> FAILED: gvkdump produced nothing; is ModelledKinds empty?" >&2
  exit 1
fi

missing=$(comm -23 <(sort -u "$rendered") "$modelled" || true)

echo "    $(wc -l < "$rendered") shipped shapes, $(wc -l < "$modelled") modelled shapes."

if [[ -n $missing ]]; then
  echo "" >&2
  echo "==> FAILED: these shapes are shipped by a chart but NOT modelled:" >&2
  while IFS= read -r gvk; do
    [[ -n $gvk ]] && echo "      $gvk" >&2
  done <<< "$missing"
  cat >&2 <<'HINT'

    Consequence if this passes: the Enrollment that applies this object stays
    Applied but Health=Unknown, so Ready=False, so everything that depends on it
    through dependsOn stops, on a cluster where everything is running.

    Fix: add an entry in internal/probe/health.go, WITH ITS REASON.
      - no status (pure configuration)         -> statelessKinds
      - a condition that means health          -> conditionTable (name which one)
      - a workload to assess                   -> a case in Assess's switch
                                                  + the key in workloadKinds
HINT
  exit 1
fi

echo "==> OK: every shipped shape is modelled."
