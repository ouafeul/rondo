#!/usr/bin/env bash
# ==============================================================================
# rondo-sandbox.sh — the RONDO component sandbox, embedded in the rondo-sandbox binary
# (cmd/rondo-sandbox), which runs it with the user's arguments.
#
# The rondo CLI commands, on k3d clusters on the workstation; the site is rondo-sandbox.yaml,
# with rondo.yaml's keys:
#
#   rondo-sandbox deploy                     # the management cluster, RONDO
#   rondo-sandbox deploy downstreams         # one k3d cluster per downstream
#   rondo-sandbox component install ../acme # the component, and RONDO's verdict
#
# This script holds the infrastructure: Docker network, PKI, stand-in artifact mirror
# (zot + nginx), k3d clusters, machines of machine components (containers), RONDO images
# pulled from their registry (or built from an RONDO clone). What speaks RONDO is `rondo-sandbox engine`
# (internal/sandbox): the CLI's own code.
#
# Documentation: docs/src/content/docs/components/sandbox.mdx.
# ==============================================================================
set -euo pipefail

ENGINE="${RONDO_SANDBOX_BIN:?this script is run by the rondo-sandbox binary}"

# Absolute state path; GNU realpath -m when present, plain prefixing otherwise (macOS).
abspath() { realpath -m "$1" 2>/dev/null || case "$1" in /*) printf '%s\n' "$1" ;; *) printf '%s/%s\n' "$PWD" "$1" ;; esac; }
STATE="$(abspath "${RONDO_SANDBOX_HOME:-${HOME}/.rondo-sandbox}")"

# The deployed RONDO, which the binary resolves for deploy: its tree (the one it embeds, or an
# RONDO clone), and its images (pulled from their registry, or built from the clone).
# The other commands read the one from the last deploy.
RONDO_SOURCE="${RONDO_SANDBOX_RONDO_SOURCE:-}"
RONDO_DIR="${RONDO_SANDBOX_RONDO_DIR:-$(cat "${STATE}/rondo-dir" 2>/dev/null || true)}"
RONDO_REGISTRY="${RONDO_SANDBOX_RONDO_REGISTRY:-}"
RONDO_VERSION="${RONDO_SANDBOX_RONDO_VERSION:-$(cat "${STATE}/rondo-version" 2>/dev/null || true)}"
CONFIG="${RONDO_SANDBOX_CONFIG:-}"
SUBNET="${RONDO_SANDBOX_SUBNET:-172.28.0.0/24}"
REGISTRY_PORT="${RONDO_SANDBOX_REGISTRY_PORT:-5100}"
K3S_IMAGE="${RONDO_SANDBOX_K3S_IMAGE:-}"
ZOT_IMAGE="ghcr.io/project-zot/zot:v2.1.8"
FRONT_IMAGE="nginx:1.27-alpine"

NETWORK="rondo-sandbox"
ZOT="rondo-sandbox-zot"
FRONT="rondo-sandbox-mirror"
PORTS="rondo-sandbox-ports"
TOFU_STATE_REPO="rondo-tofu-state-local"
BIND="${RONDO_SANDBOX_BIND:-127.0.0.1}"
MIRROR_HOST="mirror.sandbox.test"
CLUSTER_PREFIX="rondo-sbx"

# Fixed addresses below .128, Docker's own (k3d nodes) above: --ip-range keeps them apart.
IP_PREFIX="${SUBNET%.*}"
ZOT_IP="${IP_PREFIX}.2"
FRONT_IP="${IP_PREFIX}.3"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "${BLUE}[INFO]${NC}  $*" >&2; }
log_ok()    { echo -e "${GREEN}[OK]${NC}    $*" >&2; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*" >&2; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

die() { log_error "$@"; exit 1; }

usage() {
    cat <<EOF
Usage: rondo-sandbox <command> [options]   (rondo-sandbox --help: all commands)

  deploy                            the management cluster (k3d), RONDO, its vSphere Targets, the site Secrets
  deploy downstreams [target…]      one k3d cluster per downstream of rondo-sandbox.yaml, rke2 Target and base layer
  component install [directory]     validates, publishes, installs the component and awaits RONDO's verdict
      --fresh                       uninstalls the component and recreates its machines before reinstalling it
      --no-build                    does not build the component's images (rondo-sandbox.yaml)
      --timeout <duration>          maximum wait for the verdict (default 30m)
      --max-failures <n>            Job failures tolerated per Enrollment (default 2)
  component uninstall <name>        uninstalls a component, its workloads and its machines
  destroy downstreams [target…]     removes downstreams and their clusters
  destroy [--purge]                 deletes clusters, machines and the mirror; --purge also empties the cache
  logs <enrollment>                 log of an Enrollment's last Job

Variables: RONDO_SANDBOX_RONDO_REGISTRY: the registry of RONDO's images,
           RONDO_SANDBOX_HOME (${STATE}), RONDO_SANDBOX_SUBNET (${SUBNET}),
           RONDO_SANDBOX_REGISTRY_PORT (${REGISTRY_PORT}), RONDO_SANDBOX_K3S_IMAGE (k3d's default),
           RONDO_SANDBOX_BIND (${BIND}): the workstation address the component's ports are published on
EOF
}

check_prereqs() {
    local missing=()
    local tools=(docker k3d kubectl helm openssl curl)
    [[ "${RONDO_SOURCE}" == clone ]] && tools+=(git)
    for tool in "${tools[@]}"; do
        command -v "${tool}" >/dev/null || missing+=("${tool}")
    done
    [[ ${#missing[@]} -eq 0 ]] || die "missing tools: ${missing[*]}"
    docker info >/dev/null 2>&1 || die "Docker unreachable (is the daemon running, is the user in the docker group?)"
    [[ "${SUBNET}" == *.0/24 ]] || die "RONDO_SANDBOX_SUBNET must be a /24 (${SUBNET})"
    [[ -n "${RONDO_DIR}" && -d "${RONDO_DIR}/platform" ]] \
        || die "RONDO tree not found (${RONDO_DIR:-?}): run \`rondo-sandbox deploy\` first"
    drop_pushed_images
    check_disk
}

engine() {
    "${ENGINE}" engine --state "${STATE}" --rondo-dir "${RONDO_DIR}" "$@"
}

# --- stand-in artifact mirror -------------------------------------------------

ensure_network() {
    docker network inspect "${NETWORK}" >/dev/null 2>&1 && return
    docker network create --subnet "${SUBNET}" --ip-range "${IP_PREFIX}.128/25" "${NETWORK}" >/dev/null \
        || die "network ${SUBNET} unavailable (overlaps an existing network? RONDO_SANDBOX_SUBNET changes it)"
    log_ok "network ${NETWORK} (${SUBNET})"
}

ensure_pki() {
    local dir="${STATE}/pki"
    [[ -f "${dir}/tls.crt" ]] && return
    mkdir -p "${dir}"
    openssl req -x509 -newkey rsa:3072 -nodes -days 3650 -subj "/CN=RONDO sandbox CA" \
        -keyout "${dir}/ca.key" -out "${dir}/ca.pem" \
        -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -subj "/CN=${MIRROR_HOST}" \
        -keyout "${dir}/tls.key" -out "${dir}/tls.csr" 2>/dev/null
    openssl x509 -req -in "${dir}/tls.csr" -CA "${dir}/ca.pem" -CAkey "${dir}/ca.key" -CAcreateserial \
        -days 825 -out "${dir}/tls.crt" -extfile <(printf '%s\n' \
            "subjectAltName=DNS:${MIRROR_HOST},DNS:localhost,IP:127.0.0.1" \
            "basicConstraints=CA:FALSE" "keyUsage=digitalSignature,keyEncipherment" \
            "extendedKeyUsage=serverAuth") 2>/dev/null
    chmod 600 "${dir}"/*.key
    log_ok "sandbox PKI (${dir})"
}

# write_if_changed <file>: writes stdin, returns 0 if the content changed.
write_if_changed() {
    local dest="$1" tmp
    tmp="$(mktemp)"
    cat > "${tmp}"
    if [[ -f "${dest}" ]] && cmp -s "${tmp}" "${dest}"; then
        rm -f "${tmp}"
        return 1
    fi
    mv "${tmp}" "${dest}"
    chmod 644 "${dest}"
}

# ensure_container <name> <config changed 0|1> <docker run args...>
ensure_container() {
    local name="$1" changed="$2"
    shift 2
    local state
    state="$(docker inspect -f '{{.State.Running}}' "${name}" 2>/dev/null || echo absent)"
    case "${state}" in
        true)  if [[ "${changed}" == 1 ]]; then docker restart "${name}" >/dev/null; fi ;;
        false) docker start "${name}" >/dev/null ;;
        *)     docker run -d --name "${name}" --restart unless-stopped --network "${NETWORK}" "$@" >/dev/null ;;
    esac
}

ensure_registry() {
    local conf="${STATE}/registry" changed
    mkdir -p "${conf}" "${STATE}/generic"
    # zot: the component's repositories (pushed) and, on demand, images from the Internet,
    # kept in cache. The order of upstreams settles an ambiguous path.
    changed=0
    write_if_changed "${conf}/zot.json" <<EOF && changed=1
{
  "distSpecVersion": "1.1.1",
  "storage": {"rootDirectory": "/var/lib/registry", "gc": false, "dedupe": false},
  "http": {"address": "0.0.0.0", "port": "5000"},
  "log": {"level": "warn"},
  "extensions": {
    "sync": {
      "enable": true,
      "registries": [
        {"urls": ["https://registry-1.docker.io"], "onDemand": true, "tlsVerify": true, "maxRetries": 2, "retryDelay": "5s"},
        {"urls": ["https://quay.io"], "onDemand": true, "tlsVerify": true, "maxRetries": 2, "retryDelay": "5s"},
        {"urls": ["https://registry.k8s.io"], "onDemand": true, "tlsVerify": true, "maxRetries": 2, "retryDelay": "5s"},
        {"urls": ["https://ghcr.io"], "onDemand": true, "tlsVerify": true, "maxRetries": 2, "retryDelay": "5s"}
      ]
    }
  }
}
EOF
    ensure_container "${ZOT}" "${changed}" --ip "${ZOT_IP}" \
        -p "127.0.0.1:${REGISTRY_PORT}:5000" \
        -v "${conf}/zot.json:/etc/zot/config.json:ro" -v "${ZOT}:/var/lib/registry" \
        "${ZOT_IMAGE}"

    # The front end: a single host for the whole mirror — OCI to zot, the rest as files.
    # An on-demand sync of a large image takes minutes: long timeouts.
    changed=0
    write_if_changed "${conf}/nginx.conf" <<EOF && changed=1
worker_processes 1;
events { worker_connections 512; }
http {
  server {
    listen 443 ssl;
    server_name ${MIRROR_HOST};
    ssl_certificate     /etc/nginx/tls/tls.crt;
    ssl_certificate_key /etc/nginx/tls/tls.key;
    client_max_body_size 0;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_read_timeout 900s;
    location /v2/ {
      proxy_pass http://${ZOT_IP}:5000;
      proxy_set_header Host \$http_host;
      proxy_set_header X-Forwarded-Proto https;
    }
    location /files/ {
      alias /srv/generic/;
      autoindex on;
    }
    # The machines' tofu state (http backend), written with PUT: nginx takes it as WebDAV.
    location /files/${TOFU_STATE_REPO}/ {
      alias /srv/state/;
      dav_methods PUT DELETE;
      create_full_put_path on;
    }
$(cat "${STATE}/rpm-proxy.conf" 2>/dev/null)
  }
}
EOF
    # Recreated when its mounts change (label); the tofu state lives with it, like the clusters.
    if [[ "$(docker inspect -f '{{index .Config.Labels "rondo-sandbox.front"}}' "${FRONT}" 2>/dev/null)" != 2 ]]; then
        docker rm -f "${FRONT}" >/dev/null 2>&1 || true
    fi
    ensure_container "${FRONT}" "${changed}" --ip "${FRONT_IP}" --network-alias "${MIRROR_HOST}" \
        --label rondo-sandbox.front=2 --tmpfs /srv/state:mode=1777 \
        -v "${conf}/nginx.conf:/etc/nginx/nginx.conf:ro" \
        -v "${STATE}/pki/tls.crt:/etc/nginx/tls/tls.crt:ro" \
        -v "${STATE}/pki/tls.key:/etc/nginx/tls/tls.key:ro" \
        -v "${STATE}/generic:/srv/generic:ro" \
        "${FRONT_IMAGE}"

    local i
    for i in $(seq 1 30); do
        curl -fsS "http://127.0.0.1:${REGISTRY_PORT}/v2/" >/dev/null 2>&1 && break
        [[ ${i} -eq 30 ]] && die "zot does not respond (docker logs ${ZOT})"
        sleep 1
    done
    log_ok "sandbox mirror: https://${MIRROR_HOST} (${FRONT_IP}), zot on 127.0.0.1:${REGISTRY_PORT}"
}

# --- workstation ports --------------------------------------------------------

# The ports rondo-sandbox.yaml forwards, published on the workstation by an nginx on the
# sandbox network (engine plan writes its configuration). Docker does not change a container's
# ports: another list, or another configuration, recreates it.
ensure_ports() {
    local dir="${STATE}/ports" wanted signature current p publish=()
    wanted="$(cat "${dir}/ports.txt" 2>/dev/null || true)"
    if [[ -z "${wanted}" ]]; then
        docker rm -f "${PORTS}" >/dev/null 2>&1 || true
        return
    fi
    signature="${BIND} $(echo "${wanted}" | xargs) $(sha256sum < "${dir}/nginx.conf" | cut -c1-12)"
    current="$(docker inspect -f '{{index .Config.Labels "rondo-sandbox.ports"}}' "${PORTS}" 2>/dev/null || true)"
    if [[ "${current}" == "${signature}" ]]; then
        docker start "${PORTS}" >/dev/null
        return
    fi
    docker rm -f "${PORTS}" >/dev/null 2>&1 || true
    for p in ${wanted}; do
        publish+=(-p "${BIND}:${p%/*}:${p}")
    done
    docker run -d --name "${PORTS}" --restart unless-stopped --network "${NETWORK}" \
        --label "rondo-sandbox.ports=${signature}" ${publish[@]+"${publish[@]}"} \
        -v "${dir}/nginx.conf:/etc/nginx/nginx.conf:ro" "${FRONT_IMAGE}" >/dev/null \
        || die "workstation ports ($(echo "${wanted}" | xargs)): is one already taken? (ss -ltnu)"
    log_ok "workstation ports published on ${BIND}: $(echo "${wanted}" | xargs)"
}

# --- machines -----------------------------------------------------------------

# The machines of machine components (archetypes B and C): one container per VM, named and
# addressed the way terraform/vm names and addresses the VM, systemd as PID 1 and sshd, like a
# template clone once cloud-init has run. engine plan writes their list, that of all installed
# components (machines.txt). A machine keeps what was installed on it as long as its IP does not
# change, like a VM; component install --fresh recreates the component's machines blank.
reconcile_machines() {
    local fresh="${1:-}" image container name ip cpus mem comp group current wanted=" " max
    image="127.0.0.1:${REGISTRY_PORT}/rondo-docker-local/rondo-sandbox-machine:${RONDO_VERSION}"
    # Docker refuses --cpus above its own CPUs, fewer than the workstation's in a Docker VM.
    max="$(docker info -f '{{.NCPU}}' 2>/dev/null || echo 1)"
    while read -r container name ip cpus mem comp group; do
        [[ -n "${container}" ]] || continue
        wanted+="${container} "
        (( cpus > max )) && cpus="${max}"
        current="$(docker inspect -f '{{index .Config.Labels "rondo-sandbox.machine"}}' "${container}" 2>/dev/null || true)"
        if [[ -n "${current}" && ( "${fresh}" == "${comp}" || "${current}" != "${ip}" ) ]]; then
            docker rm -f "${container}" >/dev/null
            current=""
        fi
        if [[ -n "${current}" ]]; then
            docker update --cpus "${cpus}" --memory "${mem}m" --memory-swap "${mem}m" "${container}" >/dev/null \
                || log_warn "machine ${name}: size unchanged (docker update)"
            docker start "${container}" >/dev/null
            continue
        fi
        docker run -d --name "${container}" --hostname "${name}" --restart unless-stopped \
            --network "${NETWORK}" --ip "${ip}" --add-host "${MIRROR_HOST}:${FRONT_IP}" \
            --label "rondo-sandbox.machine=${ip}" --cpus "${cpus}" --memory "${mem}m" --memory-swap "${mem}m" \
            --cgroupns=private --tmpfs /run --tmpfs /run/lock \
            --cap-add SYS_ADMIN --security-opt apparmor=unconfined --security-opt label=disable \
            -e RONDO_COMPONENT="${comp}" -e RONDO_GROUP="${group}" \
            -v "${STATE}/pki/ca.pem:/etc/rondo-machine/ca.pem:ro" \
            -v "${STATE}/machines/authorized_key:/etc/rondo-machine/authorized_key:ro" \
            "${image}" >/dev/null || die "machine ${name}: start failed"
        log_ok "machine ${name} (${ip}, ${cpus} CPU, ${mem} MB)"
    done < "${STATE}/machines.txt"
    for container in $(docker ps -a --filter label=rondo-sandbox.machine --format '{{.Names}}'); do
        [[ "${wanted}" == *" ${container} "* ]] && continue
        docker rm -f "${container}" >/dev/null && log_ok "machine ${container#"${CLUSTER_PREFIX}"-machine-} deleted"
    done
}

remove_machines() {
    local container
    for container in $(docker ps -a --filter label=rondo-sandbox.machine --format '{{.Names}}'); do
        docker rm -f "${container}" >/dev/null && log_ok "machine ${container#"${CLUSTER_PREFIX}"-machine-} deleted"
    done
}

# --- clusters -----------------------------------------------------------------

existing_clusters() {
    k3d cluster list --no-headers 2>/dev/null | awk -v p="${CLUSTER_PREFIX}" '$1 == p || index($1, p "-") == 1 {print $1}' | sort
}

create_cluster() {
    local name="$1"
    log_info "creating cluster ${name}"
    # A k3d node has no /etc/machine-id, which an rke2 node always has: without it, a
    # DaemonSet that mounts it (fluent-bit) stays in ContainerCreating.
    mkdir -p "${STATE}/clusters/${name}"
    openssl rand -hex 16 > "${STATE}/clusters/${name}/machine-id"
    local args=(
        cluster create "${name}"
        --network "${NETWORK}" --servers 1 --agents 0 --no-lb
        --k3s-arg "--disable=traefik,servicelb,local-storage,metrics-server@server:0"
        --registry-config "${STATE}/registries.yaml"
        --volume "${STATE}/pki/ca.pem:/etc/rondo-sandbox/ca.pem@server:0"
        --volume "${STATE}/clusters/${name}/machine-id:/etc/machine-id@server:0"
        --host-alias "${FRONT_IP}:${MIRROR_HOST}"
        --kubeconfig-update-default=false --kubeconfig-switch-context=false
        --wait --timeout 180s
    )
    [[ -n "${K3S_IMAGE}" ]] && args+=(--image "${K3S_IMAGE}")
    k3d "${args[@]}" >"${STATE}/clusters/${name}.create.log" 2>&1 \
        || { tail -20 "${STATE}/clusters/${name}.create.log" >&2; die "creating cluster ${name} failed"; }
}

# Two kubeconfigs: the workstation's (127.0.0.1) and the one the management cluster's Jobs
# use to reach this cluster on the sandbox network.
write_kubeconfigs() {
    local name="$1" dir="${STATE}/clusters/$1" ip
    mkdir -p "${dir}"
    k3d kubeconfig get "${name}" | sed -E 's#https://0\.0\.0\.0:#https://127.0.0.1:#' > "${dir}/kubeconfig.yaml"
    ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${NETWORK}\").IPAddress}}" "k3d-${name}-server-0")"
    sed -E "s#server: https://[^ ]+#server: https://${ip}:6443#" "${dir}/kubeconfig.yaml" > "${dir}/kubeconfig.internal.yaml"
    chmod 600 "${dir}"/kubeconfig*.yaml
}

# ensure_clusters creates, or restarts, the clusters engine site wrote (clusters.txt); a
# cluster only goes away through destroy.
ensure_clusters() {
    local existing name
    existing="$(existing_clusters)"
    mkdir -p "${STATE}/clusters"
    write_if_changed "${STATE}/registries.yaml" <<EOF || true
mirrors:
  ${MIRROR_HOST}:
    endpoint:
      - https://${MIRROR_HOST}
configs:
  ${MIRROR_HOST}:
    tls:
      ca_file: /etc/rondo-sandbox/ca.pem
EOF
    while read -r name; do
        [[ -n "${name}" ]] || continue
        if grep -qx "${name}" <<<"${existing}"; then
            k3d cluster start "${name}" >/dev/null 2>&1 || true
        else
            create_cluster "${name}"
        fi
        write_kubeconfigs "${name}"
        log_ok "cluster ${name}"
    done < "${STATE}/clusters.txt"
}

# --- RONDO images ---------------------------------------------------------------

rondo_images() {
    if [[ "${RONDO_SOURCE}" == clone ]]; then
        build_rondo_images
    else
        pull_rondo_images
    fi
}

# The controller and toolbox CI published for this version, copied from their registry to
# zot once and for all: a published version does not change.
# The RONDO images the site asks for (engine site): the controller and the toolbox, and the
# sandbox machine when it has a vSphere Target.
rondo_image_names() {
    cat "${STATE}/rondo-images.txt"
}

pull_rondo_images() {
    local image src ref had
    for image in $(rondo_image_names); do
        src="${RONDO_REGISTRY}/rondo-${image}:${RONDO_VERSION}"
        ref="127.0.0.1:${REGISTRY_PORT}/rondo-docker-local/rondo-${image}:${RONDO_VERSION}"
        if in_zot "rondo-docker-local/rondo-${image}" "${RONDO_VERSION}"; then
            continue
        fi
        log_info "image ${src}"
        had="$(docker image inspect -f '{{.Id}}' "${src}" 2>/dev/null || true)"
        docker pull -q "${src}" >/dev/null 2>&1 \
            || die "${src} not found or denied: docker login ${RONDO_REGISTRY%%/*} (registry credentials), --rondo-version <published version>, or --rondo-from <RONDO clone> to build it"
        docker tag "${src}" "${ref}"
        docker push -q "${ref}" >/dev/null || die "push of ${ref} failed"
        # The workstation keeps what it had: only the copy pulled here goes.
        docker image rm "${ref}" >/dev/null 2>&1 || true
        [[ -n "${had}" ]] || docker image rm "${src}" >/dev/null 2>&1 || true
    done
    log_ok "RONDO images ${RONDO_VERSION} (${RONDO_REGISTRY})"
}

# in_zot <repository> <tag>: does zot already hold this image? Its tag list, not the manifest,
# which would make zot look for a missing image on docker.io, quay.io…
in_zot() {
    curl -fsS "http://127.0.0.1:${REGISTRY_PORT}/v2/$1/tags/list" 2>/dev/null | grep -qF "\"$2\""
}

# The RONDO version tested from a clone: the clone's commit, and a digest of what is not
# committed in it. Same source, same tag: the operator only restarts if RONDO changed.
rondo_version() {
    local commit dirty
    commit="$(git -C "${RONDO_DIR}" rev-parse --short=12 HEAD 2>/dev/null)" || { echo "sandbox-nogit"; return; }
    dirty="$( (git -C "${RONDO_DIR}" diff HEAD; git -C "${RONDO_DIR}" ls-files --others --exclude-standard) | sha256sum | cut -c1-8)"
    if git -C "${RONDO_DIR}" diff --quiet HEAD && [[ -z "$(git -C "${RONDO_DIR}" ls-files --others --exclude-standard)" ]]; then
        echo "sandbox-${commit}"
    else
        echo "sandbox-${commit}-${dirty}"
    fi
}

# The controller and toolbox of THIS clone: the component is tested against this RONDO version.
build_rondo_images() {
    local log="${STATE}/rondo-images.log" image
    : > "${log}"
    RONDO_VERSION="$(rondo_version)"
    for image in $(rondo_image_names); do
        log_info "image rondo-${image} (log: ${log})"
        docker build --progress=plain -f "${RONDO_DIR}/build/${image}/Dockerfile" \
            --build-arg VERSION="${RONDO_VERSION}" --build-arg COMMIT="$(git -C "${RONDO_DIR}" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
            -t "rondo-sandbox/rondo-${image}:build" "${RONDO_DIR}" >>"${log}" 2>&1 \
            || { tail -30 "${log}" >&2; die "building rondo-${image} failed"; }
    done
    for image in $(rondo_image_names); do
        local ref="127.0.0.1:${REGISTRY_PORT}/rondo-docker-local/rondo-${image}:${RONDO_VERSION}"
        docker tag "rondo-sandbox/rondo-${image}:build" "${ref}"
        docker push -q "${ref}" >/dev/null || die "push of ${ref} failed"
    done
    drop_pushed_images
    log_ok "RONDO images ${RONDO_VERSION}"
}

# A 127.0.0.1:<port>/… name only serves to push to zot, which keeps the image: the local copy
# of each RONDO version (700 MB) would fill the disk, and the kubelet evicts pods
# above 85%. The build cache (rondo-sandbox/*:build) stays.
drop_pushed_images() {
    local refs
    refs="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep "^127\.0\.0\.1:${REGISTRY_PORT}/" || true)"
    [[ -z "${refs}" ]] && return
    # shellcheck disable=SC2086 # one image reference per word
    docker image rm ${refs} >/dev/null 2>&1 || true
}

# Docker's disk holds the k3d nodes' images: above 85%, their kubelet goes into
# DiskPressure, evicts pods and schedules no more. Better say so before deploying.
check_disk() {
    local root used
    root="$(docker info -f '{{.DockerRootDir}}' 2>/dev/null)" || return 0
    # GNU df only; elsewhere (macOS, where Docker's disk is inside its VM anyway) no check.
    used="$( (df --output=pcent "${root}" 2>/dev/null || true) | tail -1 | tr -dc '0-9')"
    [[ -n "${used}" ]] || return 0
    local hint="docker builder prune (build cache), docker image prune, rondo-sandbox destroy --purge (sandbox cache)"
    if (( used >= 85 )); then
        die "Docker's disk (${root}) is ${used}% full: above 85%, the kubelet evicts pods; free some space: ${hint}"
    fi
    if (( used >= 80 )); then
        log_warn "Docker's disk (${root}) is ${used}% full: at 85%, the kubelet will evict pods (${hint})"
    fi
}

# --- commands ------------------------------------------------------------------

# deploy: rondo deploy on the k3d management cluster. The site is rondo-sandbox.yaml (-c).
cmd_deploy() {
    [[ $# -eq 0 ]] || die "deploy takes no argument (deploy downstreams [target…] for the downstreams)"
    check_prereqs
    mkdir -p "${STATE}"
    if [[ "${RONDO_SOURCE}" == clone ]]; then
        log_ok "RONDO deployed: clone ${RONDO_DIR} ($(git -C "${RONDO_DIR}" rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?'))"
    else
        log_ok "RONDO deployed: ${RONDO_VERSION} (images ${RONDO_REGISTRY})"
    fi
    echo "${RONDO_DIR}" > "${STATE}/rondo-dir"
    engine site --config "${CONFIG}" --ip-prefix "${IP_PREFIX}"
    ensure_network
    ensure_pki
    ensure_registry
    ensure_clusters
    rondo_images
    echo "${RONDO_VERSION}" > "${STATE}/rondo-version"
    engine publish-rondo --registry "127.0.0.1:${REGISTRY_PORT}" --rondo-version "${RONDO_VERSION}"
    drop_pushed_images
    engine deploy --rondo-version "${RONDO_VERSION}"
}

# deploy downstreams: rondo deploy downstreams, one k3d cluster per downstream.
cmd_deploy_downstreams() {
    check_prereqs
    [[ -n "${RONDO_VERSION}" ]] || die "no RONDO deployed: run \`rondo-sandbox deploy\` first"
    engine site --config "${CONFIG}" --ip-prefix "${IP_PREFIX}" --downstreams "$@"
    ensure_network
    ensure_registry
    ensure_clusters
    engine downstreams --rondo-version "${RONDO_VERSION}" "$@"
}

# component install: what the component's CI publishes, then rondo component install --from.
cmd_component_install() {
    local dir="." timeout="30m" max_failures="2" fresh="" publish_opts=() install_opts=() name
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --fresh)        install_opts+=(--fresh); fresh=1; shift ;;
            --no-build)     publish_opts+=(--no-build); shift ;;
            --timeout)      timeout="$2"; shift 2 ;;
            --max-failures) max_failures="$2"; shift 2 ;;
            -*)             die "unknown option: $1" ;;
            *)              dir="$1"; shift ;;
        esac
    done
    [[ -d "${dir}" ]] || die "${dir} is not a directory"
    check_prereqs
    [[ -f "${STATE}/site.json" ]] || die "no sandbox: run \`rondo-sandbox deploy\` first"
    engine plan "${dir}" --ip-prefix "${IP_PREFIX}"
    name="$(cat "${STATE}/installing")"
    ensure_registry
    ensure_ports
    reconcile_machines "${fresh:+${name}}"
    # ${a[@]+"${a[@]}"}: an empty array is unbound under set -u before bash 4.4 (macOS's bash 3.2).
    engine publish "${name}" --registry "127.0.0.1:${REGISTRY_PORT}" ${publish_opts[@]+"${publish_opts[@]}"}
    drop_pushed_images
    engine install "${name}" --timeout "${timeout}" --max-failures "${max_failures}" \
        ${install_opts[@]+"${install_opts[@]}"}
}

cmd_component_uninstall() {
    [[ $# -eq 1 ]] || die "component uninstall <name>"
    check_prereqs
    engine uninstall "$1"
    reconcile_machines
    ensure_ports
    ensure_registry
}

cmd_logs() {
    [[ $# -eq 1 ]] || die "logs <enrollment>"
    local kc="${STATE}/clusters/${CLUSTER_PREFIX}/kubeconfig.yaml" job
    job="$(kubectl --kubeconfig "${kc}" -n rondo-system get enrollments.rondo.ouafeul.github.io "$1" \
        -o jsonpath='{.status.lastAttempt.jobName}')"
    [[ -n "${job}" ]] || die "no Job for Enrollment $1"
    kubectl --kubeconfig "${kc}" -n rondo-system logs "job/${job}"
}

# destroy downstreams: rondo destroy downstreams, with their k3d clusters deleted.
cmd_destroy_downstreams() {
    local name
    check_prereqs
    engine destroy-downstreams "$@"
    while read -r name; do
        [[ -n "${name}" ]] || continue
        k3d cluster delete "${name}" >/dev/null 2>&1 && log_ok "cluster ${name} deleted"
        rm -rf "${STATE}/clusters/${name}" "${STATE}/clusters/${name}.create.log"
    done < "${STATE}/clusters.txt"
}

cmd_destroy() {
    local purge="${1:-}" name
    for name in $(existing_clusters); do
        k3d cluster delete "${name}" >/dev/null 2>&1 && log_ok "cluster ${name} deleted"
    done
    remove_machines
    docker rm -f "${PORTS}" "${FRONT}" "${ZOT}" >/dev/null 2>&1 || true
    docker network rm "${NETWORK}" >/dev/null 2>&1 || true
    rm -rf "${STATE}/clusters" "${STATE}/trees" "${STATE}/site.json" "${STATE}/components" \
        "${STATE}/clusters.txt" "${STATE}/machines.txt" "${STATE}/rpm-proxy.conf" "${STATE}/installing" \
        "${STATE}/rondo-images.txt" "${STATE}/rondo-version" "${STATE}/plan.json"
    if [[ "${purge}" == "--purge" ]]; then
        docker volume rm "${ZOT}" >/dev/null 2>&1 || true
        # Only what the sandbox writes: RONDO_SANDBOX_HOME may name a directory it shares.
        rm -rf "${STATE:?}/bin" "${STATE:?}/generic" "${STATE:?}/pki" "${STATE:?}/registry" \
            "${STATE:?}/registries.yaml" "${STATE:?}/rondo-images.log" \
            "${STATE:?}/ports" "${STATE:?}/go.work" "${STATE:?}/rondo" "${STATE:?}/rondo-dir" \
            "${STATE:?}/machines"
        rmdir "${STATE}" 2>/dev/null || true
        log_ok "sandbox purged (image and chart cache included)"
        return
    fi
    log_ok "sandbox deleted; the cache (volume ${ZOT}) stays for the next deploy"
}

main() {
    local cmd="${1:-}" sub="${2:-}"
    case "${cmd} ${sub}" in
        "deploy downstreams")   shift 2; cmd_deploy_downstreams "$@" ;;
        "destroy downstreams")  shift 2; cmd_destroy_downstreams "$@" ;;
        "component install")    shift 2; cmd_component_install "$@" ;;
        "component uninstall")  shift 2; cmd_component_uninstall "$@" ;;
        *)
            [[ $# -gt 0 ]] && shift
            case "${cmd}" in
                deploy)  cmd_deploy "$@" ;;
                destroy) cmd_destroy "$@" ;;
                logs)    cmd_logs "$@" ;;
                ""|-h|--help|help) usage ;;
                *) usage; exit 1 ;;
            esac ;;
    esac
}

main "$@"
