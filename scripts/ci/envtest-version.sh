#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$0")/../.." && pwd)"

DEPS="${ROOT}/manifests/dependencies.yaml"
[ -f "${DEPS}" ] || { echo "FAILED: ${DEPS} not found" >&2; exit 1; }

version="$(awk '
  /^[^[:space:]#]/ {
    in_binaries = ($0 ~ /^binaries:[[:space:]]*$/); name = ""; version = ""; next
  }
  !in_binaries { next }

  /^[[:space:]]*-[[:space:]]/ { name = ""; version = "" }

  {
    field = $0
    sub(/^[[:space:]]*-[[:space:]]+/, "", field)
    sub(/^[[:space:]]+/, "", field)
    if (field ~ /^name:/)    { name    = scalar(field) }
    if (field ~ /^version:/) { version = scalar(field) }
    if (name == "k3s" && version != "") {
      if (match(version, /[0-9]+\.[0-9]+/)) {
        print substr(version, RSTART, RLENGTH)
        exit 0
      }
      print "FAILED: unreadable k3s version: " version > "/dev/stderr"
      exit 2
    }
  }

  function scalar(f) {
    sub(/^[^:]+:[[:space:]]*/, "", f)
    gsub(/"/, "", f)
    sub(/[[:space:]]+$/, "", f)
    return f
  }
' "${DEPS}")"

if [ -z "${version}" ]; then
  echo "FAILED: could not determine the apiserver version from ${DEPS}" >&2
  exit 1
fi

printf '%s\n' "${version}"
