#!/usr/bin/env bash
# Copies one RONDO release into a site mirror, from a machine that reaches both GitHub/GHCR and
# the mirror. The target is any OCI registry and any HTTP file server (or S3 bucket, via the
# `aws` CLI, or a directory).
#
#   scripts/mirror-release.sh --version v1.0.0 \
#       --registry registry.site/rondo --files-url https://files.site/mirror \
#       [--rpm-url https://rpm.site/rondo] [--channel stable] [--dry-run]
#
# Copies:
#   images   ghcr.io/ouafeul/rondo-{controller,toolbox,component-toolbox,sandbox-machine}:<tag>
#            -> <registry>/rondo-<name>:<tag>                                   (skopeo)
#   charts   oci://ghcr.io/ouafeul/charts/<chart>, every apps/ chart at <tag>
#            -> oci://<registry>/charts/<chart>                                  (helm pull/push)
#   files    the release assets, checked against SHA256SUMS
#            -> <files-url>/rondo/<tag>/<asset>, plus vm.tar.gz under tofu-modules/vm/<tag>/
#               and rondo-ansible.tar.gz under ansible/rondo/<tag>/
#   channel  with --channel: <files-url>/rondo/<channel>.json, written LAST so that a site never
#            resolves a version whose files are missing
#   rpm/deb  with --rpm-url: <rpm-url>/rondo/<tag>/, with repodata/ built by createrepo_c when it
#            is installed
#
# Every uploaded file gets a <file>.sha256 sidecar; a file whose sidecar matches is not sent again.
# Environment: MIRROR_USER / MIRROR_PASSWORD, MIRROR_CA_CERT (PEM file); MIRROR_REGISTRY,
# MIRROR_FILES_URL, MIRROR_RPM_URL stand for the flags; RONDO_REPO (default ouafeul/rondo) and
# RONDO_SOURCE_REGISTRY (default ghcr.io/ouafeul) name the source.
# --dry-run prints the plan ("PLAN ..." lines) and changes nothing; it still reads SHA256SUMS and
# the apps/ charts of the tag.
set -euo pipefail

RONDO_REPO="${RONDO_REPO:-ouafeul/rondo}"
SOURCE_REGISTRY="${RONDO_SOURCE_REGISTRY:-ghcr.io/ouafeul}"
IMAGES=(rondo-controller rondo-toolbox rondo-component-toolbox rondo-sandbox-machine)

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

version="" registry="${MIRROR_REGISTRY:-}" files_url="${MIRROR_FILES_URL:-}"
rpm_url="${MIRROR_RPM_URL:-}" channel="" dry_run=0

while [ $# -gt 0 ]; do
  case "$1" in
    --version)   version="${2:?--version needs a value}"; shift 2 ;;
    --registry)  registry="${2:?--registry needs a value}"; shift 2 ;;
    --files-url) files_url="${2:?--files-url needs a value}"; shift 2 ;;
    --rpm-url)   rpm_url="${2:?--rpm-url needs a value}"; shift 2 ;;
    --channel)   channel="${2:?--channel needs a value}"; shift 2 ;;
    --dry-run)   dry_run=1; shift ;;
    -h|--help)   usage 0 ;;
    *) printf 'ERROR: unknown argument: %s\n' "$1" >&2; usage 1 ;;
  esac
done

[ -n "${version}" ] || die "--version is required"
case "${version}" in v*) ;; *) version="v${version}" ;; esac
printf '%s' "${version}" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' \
  || die "${version} is not a vMAJOR.MINOR.PATCH[-PRERELEASE] version"
[ -n "${registry}" ] || die "--registry (MIRROR_REGISTRY) is required"
[ -n "${files_url}" ] || die "--files-url (MIRROR_FILES_URL) is required"
if [ -n "${channel}" ]; then
  printf '%s' "${channel}" | grep -Eq '^[a-z0-9][a-z0-9._-]*$' || die "invalid channel name: ${channel}"
fi
registry="${registry#https://}"; registry="${registry#http://}"; registry="${registry%/}"
registry_host="${registry%%/*}"
files_url="${files_url%/}"
rpm_url="${rpm_url%/}"

for tool in curl git; do
  command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
done
if [ "${dry_run}" -eq 0 ]; then
  for tool in skopeo helm; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
  done
fi

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
mkdir -p "${work}/assets"

# Credentials and CA stay out of the process list: curl reads a config file, helm and skopeo a
# password on stdin; their state files live in the work directory, not in the operator's home.
curl_cfg="${work}/curl.cfg"
: > "${curl_cfg}"
chmod 600 "${curl_cfg}"
if [ -n "${MIRROR_USER:-}" ] && [ -n "${MIRROR_PASSWORD:-}" ]; then
  printf 'user = "%s:%s"\n' "${MIRROR_USER}" "${MIRROR_PASSWORD}" >> "${curl_cfg}"
fi
if [ -n "${MIRROR_CA_CERT:-}" ]; then
  [ -f "${MIRROR_CA_CERT}" ] || die "MIRROR_CA_CERT: ${MIRROR_CA_CERT} is not a file"
  printf 'cacert = "%s"\n' "${MIRROR_CA_CERT}" >> "${curl_cfg}"
  mkdir -p "${work}/certs"
  cp "${MIRROR_CA_CERT}" "${work}/certs/ca.crt"
fi
export HELM_REGISTRY_CONFIG="${work}/helm-registry.json"
authfile="${work}/auth.json"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# --- file transfer: http(s) PUT, s3:// through the aws CLI, or a directory ------------------

put_file() { # <local> <url>
  case "$2" in
    http://*|https://*) curl -fsS -K "${curl_cfg}" -T "$1" "$2" >/dev/null ;;
    s3://*)             aws s3 cp --only-show-errors "$1" "$2" ;;
    *)                  mkdir -p "$(dirname "$2")"; cp "$1" "$2" ;;
  esac
}

read_remote() { # <url>: prints the content, nothing when absent
  case "$1" in
    http://*|https://*) curl -fsS -K "${curl_cfg}" "$1" 2>/dev/null || true ;;
    s3://*)             aws s3 cp --only-show-errors "$1" - 2>/dev/null || true ;;
    *)                  cat "$1" 2>/dev/null || true ;;
  esac
}

# upload <local> <url>: skipped when <url>.sha256 holds the digest of <local>.
upload() {
  local file="$1" url="$2" digest remote
  digest="$(sha256_of "${file}")"
  remote="$(read_remote "${url}.sha256" | awk '{print $1; exit}')"
  if [ "${remote}" = "${digest}" ]; then
    log "skip ${url} (sidecar matches)"
    return 0
  fi
  log "upload ${url}"
  put_file "${file}" "${url}"
  printf '%s  %s\n' "${digest}" "$(basename "${url}")" > "${work}/sidecar"
  put_file "${work}/sidecar" "${url}.sha256"
}

# --- plan ------------------------------------------------------------------------------------

base="https://github.com/${RONDO_REPO}/releases/download/${version}"
log "reading ${base}/SHA256SUMS"
curl -fsSL -o "${work}/SHA256SUMS" "${base}/SHA256SUMS" || die "cannot download ${base}/SHA256SUMS"

assets=()
while read -r sum name; do
  [ -n "${name:-}" ] || continue
  name="${name#\*}"
  assets+=("${sum}  ${name}")
done < "${work}/SHA256SUMS"
[ "${#assets[@]}" -gt 0 ] || die "SHA256SUMS lists no asset"

log "reading the apps/ charts of ${version}"
git clone --quiet --depth 1 --branch "${version}" "https://github.com/${RONDO_REPO}.git" "${work}/src" \
  || die "cannot clone ${RONDO_REPO} at ${version}"
charts=()
for chart_yaml in "${work}"/src/apps/*/Chart.yaml; do
  [ -f "${chart_yaml}" ] || continue
  name="$(awk '/^name:/ {gsub(/["\047]/, "", $2); print $2; exit}' "${chart_yaml}")"
  chart_version="$(awk '/^version:/ {gsub(/["\047]/, "", $2); print $2; exit}' "${chart_yaml}")"
  [ -n "${name}" ] && [ -n "${chart_version}" ] || die "${chart_yaml}: no name or version"
  charts+=("${name} ${chart_version}")
done

plan_files=()   # "asset|destination url"
plan_rpm=()     # "asset|destination url"
for entry in "${assets[@]}"; do
  asset="${entry#*  }"
  case "${asset}" in
    stable.json)
      printf 'PLAN skip: %s (channels are set with --channel)\n' "${asset}"; continue ;;
    *.sha256)
      printf 'PLAN skip: %s (replaced by the sidecars)\n' "${asset}"; continue ;;
    *.rpm|*.deb)
      if [ -n "${rpm_url}" ]; then
        plan_rpm+=("${asset}|${rpm_url}/rondo/${version}/${asset}")
      else
        printf 'PLAN skip: %s (no --rpm-url)\n' "${asset}"
      fi
      continue ;;
  esac
  plan_files+=("${asset}|${files_url}/rondo/${version}/${asset}")
  case "${asset}" in
    vm.tar.gz)             plan_files+=("${asset}|${files_url}/tofu-modules/vm/${version}/${asset}") ;;
    rondo-ansible.tar.gz)  plan_files+=("${asset}|${files_url}/ansible/rondo/${version}/${asset}") ;;
  esac
done

for image in "${IMAGES[@]}"; do
  printf 'PLAN image: %s/%s:%s -> %s/%s:%s\n' "${SOURCE_REGISTRY}" "${image}" "${version}" \
    "${registry}" "${image}" "${version}"
done
for entry in ${charts[@]+"${charts[@]}"}; do
  printf 'PLAN chart: oci://%s/charts/%s %s -> oci://%s/charts/%s\n' "${SOURCE_REGISTRY}" \
    "${entry% *}" "${entry#* }" "${registry}" "${entry% *}"
done
for entry in ${plan_files[@]+"${plan_files[@]}"}; do
  printf 'PLAN file: %s -> %s\n' "${entry%%|*}" "${entry#*|}"
done
for entry in ${plan_rpm[@]+"${plan_rpm[@]}"}; do
  printf 'PLAN rpm: %s -> %s\n' "${entry%%|*}" "${entry#*|}"
done
if [ -n "${channel}" ]; then
  printf 'PLAN channel: %s/rondo/%s.json -> {"version":"%s"} (last)\n' "${files_url}" "${channel}" "${version}"
fi

if [ "${dry_run}" -eq 1 ]; then
  log "dry run: nothing copied"
  exit 0
fi

# --- copy ------------------------------------------------------------------------------------

if [ -n "${MIRROR_USER:-}" ] && [ -n "${MIRROR_PASSWORD:-}" ]; then
  log "logging in to ${registry_host}"
  skopeo_tls=()
  [ -d "${work}/certs" ] && skopeo_tls=(--cert-dir "${work}/certs")
  printf '%s' "${MIRROR_PASSWORD}" | skopeo login ${skopeo_tls[@]+"${skopeo_tls[@]}"} --authfile "${authfile}" \
    --username "${MIRROR_USER}" --password-stdin "${registry_host}"
  helm_tls=()
  [ -n "${MIRROR_CA_CERT:-}" ] && helm_tls=(--ca-file "${MIRROR_CA_CERT}")
  printf '%s' "${MIRROR_PASSWORD}" | helm registry login ${helm_tls[@]+"${helm_tls[@]}"} \
    --username "${MIRROR_USER}" --password-stdin "${registry_host}"
fi

copy_tls=()
[ -d "${work}/certs" ] && copy_tls=(--dest-cert-dir "${work}/certs")
copy_auth=()
[ -f "${authfile}" ] && copy_auth=(--authfile "${authfile}")
for image in "${IMAGES[@]}"; do
  log "image ${image}:${version}"
  skopeo copy --all ${copy_tls[@]+"${copy_tls[@]}"} ${copy_auth[@]+"${copy_auth[@]}"} \
    "docker://${SOURCE_REGISTRY}/${image}:${version}" "docker://${registry}/${image}:${version}"
done

helm_tls=()
[ -n "${MIRROR_CA_CERT:-}" ] && helm_tls=(--ca-file "${MIRROR_CA_CERT}")
mkdir -p "${work}/charts"
for entry in ${charts[@]+"${charts[@]}"}; do
  name="${entry% *}"; chart_version="${entry#* }"
  if helm show chart "oci://${registry}/charts/${name}" --version "${chart_version}" ${helm_tls[@]+"${helm_tls[@]}"} >/dev/null 2>&1; then
    log "skip chart ${name} ${chart_version} (already in the registry)"
    continue
  fi
  log "chart ${name} ${chart_version}"
  helm pull "oci://${SOURCE_REGISTRY}/charts/${name}" --version "${chart_version}" -d "${work}/charts"
  helm push "${work}/charts/${name}-${chart_version}.tgz" "oci://${registry}/charts" ${helm_tls[@]+"${helm_tls[@]}"}
done

for entry in "${assets[@]}"; do
  sum="${entry%%  *}"; asset="${entry#*  }"
  case "${asset}" in stable.json|*.sha256) continue ;; esac
  [ -f "${work}/assets/${asset}" ] && continue
  log "download ${asset}"
  curl -fSL --retry 3 -o "${work}/assets/${asset}" "${base}/${asset}"
  [ "$(sha256_of "${work}/assets/${asset}")" = "${sum}" ] \
    || die "${asset}: checksum differs from SHA256SUMS"
done

for entry in ${plan_files[@]+"${plan_files[@]}"}; do
  upload "${work}/assets/${entry%%|*}" "${entry#*|}"
done

if [ "${#plan_rpm[@]}" -gt 0 ]; then
  for entry in "${plan_rpm[@]}"; do
    upload "${work}/assets/${entry%%|*}" "${entry#*|}"
  done
  if command -v createrepo_c >/dev/null 2>&1; then
    mkdir -p "${work}/repo"
    for entry in "${plan_rpm[@]}"; do
      case "${entry%%|*}" in *.rpm) cp "${work}/assets/${entry%%|*}" "${work}/repo/" ;; esac
    done
    if compgen -G "${work}/repo/*.rpm" >/dev/null; then
      createrepo_c --quiet "${work}/repo"
      while IFS= read -r f; do
        upload "${f}" "${rpm_url}/rondo/${version}/${f#"${work}/repo/"}"
      done < <(find "${work}/repo/repodata" -type f ! -name repomd.xml)
      upload "${work}/repo/repodata/repomd.xml" "${rpm_url}/rondo/${version}/repodata/repomd.xml"
    fi
  else
    warn "createrepo_c is not installed: repodata/ is not built, the rpm directory is not a usable dnf repository"
  fi
fi

if [ -n "${channel}" ]; then
  printf '{"version":"%s"}\n' "${version}" > "${work}/channel.json"
  upload "${work}/channel.json" "${files_url}/rondo/${channel}.json"
fi

log "RONDO ${version} mirrored"
