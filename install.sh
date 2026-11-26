#!/bin/sh
# Installs the rondo CLI into $RONDO_HOME (default ~/.rondo) and links it into ~/.local/bin.
#
# Sources, in order:
#   - RONDO_MIRROR_URL set: an air-gapped mirror serving <url>/rondo/<channel>.json and
#     <url>/rondo/<version>/rondo-linux-amd64 (optional RONDO_MIRROR_USER / RONDO_MIRROR_PASSWORD,
#     RONDO_MIRROR_CA_CERT as a PEM string);
#   - otherwise GitHub Releases of ouafeul/rondo.
# RONDO_VERSION pins a version (e.g. v1.0.0); otherwise RONDO_CHANNEL (default: stable) is resolved.
set -eu

RONDO_REPO="${RONDO_REPO:-ouafeul/rondo}"
RONDO_CHANNEL="${RONDO_CHANNEL:-stable}"
RONDO_HOME="${RONDO_HOME:-$HOME/.rondo}"
export RONDO_HOME
CA_FILE="${RONDO_HOME}/ca/mirror-ca.pem"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWARN\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR\033[0m %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required."

os="$(uname -s)"; arch="$(uname -m)"
[ "$os" = "Linux" ] || warn "OS '$os' is not supported (linux/amd64 binary only)."
case "$arch" in
  x86_64|amd64) : ;;
  *) warn "architecture '$arch' is not supported (linux/amd64 binary only)." ;;
esac

mkdir -p "${RONDO_HOME}/bin" "${RONDO_HOME}/ca" "${RONDO_HOME}/cache"

set -- -fsSL
if [ -n "${RONDO_MIRROR_URL:-}" ]; then
  BASE="${RONDO_MIRROR_URL%/}/rondo"
  if [ -n "${RONDO_MIRROR_CA_CERT:-}" ]; then
    printf '%s\n' "$RONDO_MIRROR_CA_CERT" > "$CA_FILE"
    log "mirror CA written to $CA_FILE"
  fi
  [ -f "$CA_FILE" ] && set -- "$@" --cacert "$CA_FILE"
  if [ -n "${RONDO_MIRROR_USER:-}" ]; then
    set -- "$@" -u "${RONDO_MIRROR_USER}:${RONDO_MIRROR_PASSWORD:-}"
  fi
  channel_url="${BASE}/${RONDO_CHANNEL}.json"
else
  BASE="https://github.com/${RONDO_REPO}/releases/download"
  [ "$RONDO_CHANNEL" = "stable" ] || die "only the stable channel is published on GitHub; set RONDO_VERSION instead."
  channel_url="https://github.com/${RONDO_REPO}/releases/latest/download/stable.json"
fi
CURL_OPTS="$*"

# shellcheck disable=SC2086 # CURL_OPTS is a word list on purpose
dl() { curl $CURL_OPTS "$@"; }

VERSION="${RONDO_VERSION:-}"
if [ -z "$VERSION" ]; then
  log "resolving channel '$RONDO_CHANNEL'"
  VERSION="$(dl "$channel_url" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "$VERSION" ] || die "no version found in $channel_url"
fi
log "rondo version: $VERSION"

log "downloading the rondo binary"
dl "${BASE}/${VERSION}/rondo-linux-amd64" -o "${RONDO_HOME}/bin/rondo.tmp"
if dl "${BASE}/${VERSION}/SHA256SUMS" -o "${RONDO_HOME}/cache/SHA256SUMS" 2>/dev/null; then
  want="$(awk '$2 == "rondo-linux-amd64" {print $1}' "${RONDO_HOME}/cache/SHA256SUMS")"
  got="$(sha256sum "${RONDO_HOME}/bin/rondo.tmp" 2>/dev/null | awk '{print $1}')"
  if [ -n "$want" ] && [ -n "$got" ] && [ "$want" != "$got" ]; then
    rm -f "${RONDO_HOME}/bin/rondo.tmp"
    die "checksum mismatch for rondo-linux-amd64 (want $want, got $got)"
  fi
fi
mv "${RONDO_HOME}/bin/rondo.tmp" "${RONDO_HOME}/bin/rondo"
chmod +x "${RONDO_HOME}/bin/rondo"

if [ ! -f "${RONDO_HOME}/rondo.yaml" ]; then
  "${RONDO_HOME}/bin/rondo" init || warn "'rondo init' failed; run it again by hand."
fi

mkdir -p "${HOME}/.local/bin"
ln -sf "${RONDO_HOME}/bin/rondo" "${HOME}/.local/bin/rondo"

log "rondo installed: ${RONDO_HOME}/bin/rondo"
case ":${PATH}:" in
  *":${HOME}/.local/bin:"*) : ;;
  *) warn "Add ~/.local/bin to your PATH:  export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac

cat <<EOF

Next steps:
  1. Edit ${RONDO_HOME}/rondo.yaml (artifact mirror, targets, secrets).
  2. rondo deploy                       # the management cluster
  3. rondo deploy downstreams           # the downstream rke2 clusters
  4. rondo component install <name>     # components

Documentation: https://ouafeul.github.io/rondo/
EOF
