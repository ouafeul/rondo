#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

CLEAN_PKG="${1:-./cmd/rondo}"

SUSPECT='^(k8s\.io|sigs\.k8s\.io)/'
TOLERATED='^sigs\.k8s\.io/yaml($|/)'

ALLOWED_MODULES='
rondo
charm.land/bubbles/v2
charm.land/bubbletea/v2
charm.land/lipgloss/v2
github.com/atotto/clipboard
github.com/charmbracelet/colorprofile
github.com/charmbracelet/ultraviolet
github.com/charmbracelet/x/ansi
github.com/charmbracelet/x/term
github.com/charmbracelet/x/termios
github.com/charmbracelet/x/windows
github.com/clipperhouse/displaywidth
github.com/clipperhouse/uax29/v2
github.com/gabriel-vasile/mimetype
github.com/go-playground/locales
github.com/go-playground/universal-translator
github.com/go-playground/validator/v10
github.com/leodido/go-urn
github.com/lucasb-eyer/go-colorful
github.com/mattn/go-runewidth
github.com/muesli/cancelreader
github.com/rivo/uniseg
github.com/spf13/cobra
github.com/spf13/pflag
github.com/xo/terminfo
go.yaml.in/yaml/v2
go.yaml.in/yaml/v3
golang.org/x/crypto
golang.org/x/sync
golang.org/x/sys
golang.org/x/term
golang.org/x/text
sigs.k8s.io/yaml
'

echo "==> Dependency guard: ${CLEAN_PKG} without typed k8s.io"

if ! deps="$(go list -deps "$CLEAN_PKG" 2>&1)"; then
  echo "FAILED: go list -deps ${CLEAN_PKG} failed:" >&2
  echo "$deps" >&2
  exit 1
fi

dep_count="$(printf '%s\n' "$deps" | grep -c . || true)"
if [ "$dep_count" -lt 2 ]; then
  echo "FAILED: go list -deps ${CLEAN_PKG} returned only ${dep_count} package(s)," >&2
  echo "        nothing was checked. Missing package, or broken build?" >&2
  exit 1
fi

offenders="$(printf '%s\n' "$deps" | grep -E "$SUSPECT" | grep -Ev "$TOLERATED" | sort -u || true)"
if [ -n "$offenders" ]; then
  echo "FAILED: ${CLEAN_PKG} pulls in dependencies from the Kubernetes ecosystem:" >&2
  printf '%s\n' "$offenders" | sed 's/^/  /' >&2
  cat >&2 <<'EOF'

The rondo binary must stay free of client-go (CVE surface + size: it is
installed on operator workstations and scanned to zero CVEs).

Leads:
  - did an api/v1alpha1 type leak into internal/kobj, internal/bootstrap
    or internal/constructs? Those packages emit map[string]any.
  - does a stage in internal/steps import internal/controllers?
    Day-0 orchestration and the reconcilers share no code.
EOF
  exit 1
fi

if ! mods="$(go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' "$CLEAN_PKG" 2>&1)"; then
  echo "FAILED: go list -deps (modules) ${CLEAN_PKG} failed:" >&2
  echo "$mods" >&2
  exit 1
fi
mods="$(printf '%s\n' "$mods" | grep -v '^$' | sort -u)"

mod_count="$(printf '%s\n' "$mods" | grep -c . || true)"
if [ "$mod_count" -lt 1 ]; then
  echo "FAILED: no module in the closure of ${CLEAN_PKG}, nothing was checked." >&2
  exit 1
fi

allowed_re="^($(printf '%s\n' "$ALLOWED_MODULES" | grep -v '^$' | sed 's/\./\\./g' | paste -sd'|' -))$"
rogue="$(printf '%s\n' "$mods" | grep -Ev "$allowed_re" || true)"
if [ -n "$rogue" ]; then
  echo "FAILED: ${CLEAN_PKG} pulls in modules OUTSIDE the allowlist:" >&2
  printf '%s\n' "$rogue" | sed 's/^/  /' >&2
  cat >&2 <<'EOF'

This binary ships to operator workstations and passes the zero-CVE scan: every
added module is attack surface and size.

Two ways out, to be chosen deliberately:
  - the dependency does not belong here (most common case: a test-only package
    imported from production code). Remove it.
  - it is legitimate: add it to ALLOWED_MODULES in the same commit, so that
    the pull request shows it.
EOF
  exit 1
fi

echo "==> OK: ${CLEAN_PKG} is clean ($(printf '%s\n' "$deps" | wc -l | tr -d ' ') packages, ${mod_count} modules, all in the allowlist)."
