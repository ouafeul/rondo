#!/usr/bin/env bash
# ==============================================================================
# e2e-sandbox.sh — the sandbox end-to-end scenario (.github/workflows/e2e.yml), runnable on a
# workstation: RONDO built from this clone, the generic artifact mirror (zot + nginx), the test
# component (internal/sandbox/testdata/component) installed on a management cluster, a
# downstream and a container machine, and the generic mirror features checked on the way.
#
#   scripts/ci/e2e-sandbox.sh tools <dir>    k3d, kubectl, helm (linux amd64, checksums verified)
#   scripts/ci/e2e-sandbox.sh run            the scenario; exits non-zero at the first failure
#   scripts/ci/e2e-sandbox.sh collect <dir>  what explains a failure: status, Jobs, pods, mirror
#   scripts/ci/e2e-sandbox.sh cleanup        rondo-sandbox destroy --purge
#
# Variables: E2E_WORK (scratch directory, default a new temporary one), RONDO_SANDBOX_HOME
# (default ~/.rondo-sandbox), RONDO_SANDBOX_REGISTRY_PORT (5100).
# ==============================================================================
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$0")/../.." && pwd)"
WORK="${E2E_WORK:-${TMPDIR:-/tmp}/rondo-e2e}"
BIN="${WORK}/bin"
REGISTRY="127.0.0.1:${RONDO_SANDBOX_REGISTRY_PORT:-5100}"
STATE="${RONDO_SANDBOX_HOME:-${HOME}/.rondo-sandbox}"
COMPONENT="${WORK}/acme"

# The workstation tools of the sandbox. kubectl and helm are those of
# manifests/dependencies.yaml (scripts/ci/tests/test_e2e_sandbox.py keeps them equal).
K3D_VERSION="v5.8.3"
K3D_SHA256="dbaa79a76ace7f4ca230a1ff41dc7d8a5036a8ad0309e9c54f9bf3836dbe853e"
KUBECTL_VERSION="v1.34.13"
KUBECTL_SHA256="b4bb494e9466a7a94bb851414548bc0d3e2f8da90757e6eccc3371e648648591"
HELM_VERSION="v3.22.1"
HELM_SHA256="896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea"

step() { printf '\n==> %s\n' "$*" >&2; }
fail() { printf 'FAILED: %s\n' "$*" >&2; exit 1; }

# fetch <url> <sha256> <dest>
fetch() {
    curl -fsSL --retry 3 -o "$3" "$1"
    echo "$2  $3" | sha256sum -c --quiet - || fail "checksum of $1"
}

cmd_tools() {
    local dir="${1:?tools <dir>}" tmp
    mkdir -p "${dir}"
    tmp="$(mktemp -d)"
    fetch "https://github.com/k3d-io/k3d/releases/download/${K3D_VERSION}/k3d-linux-amd64" "${K3D_SHA256}" "${dir}/k3d"
    fetch "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" "${KUBECTL_SHA256}" "${dir}/kubectl"
    fetch "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" "${HELM_SHA256}" "${tmp}/helm.tgz"
    tar xzf "${tmp}/helm.tgz" -C "${tmp}"
    mv "${tmp}/linux-amd64/helm" "${dir}/helm"
    chmod +x "${dir}/k3d" "${dir}/kubectl" "${dir}/helm"
    rm -rf "${tmp}"
}

sandbox() { (cd "${COMPONENT}" && "${BIN}/rondo-sandbox" "$@"); }

# tags <repository>: the tags zot holds for it.
tags() { curl -fsS "http://${REGISTRY}/v2/$1/tags/list"; }

cmd_run() {
    mkdir -p "${BIN}"
    step "rondo-sandbox from this clone"
    (cd "${ROOT}" && go build -o "${BIN}/rondo-sandbox" ./cmd/rondo-sandbox)
    rm -rf "${COMPONENT}"
    cp -R "${ROOT}/internal/sandbox/testdata/component" "${COMPONENT}"
    export RONDO_SRC_DIR="${ROOT}"

    step "check: the component against its site, without clusters"
    sandbox check

    step "deploy: management cluster, RONDO built from the clone, vSphere Target dc1"
    sandbox deploy

    step "generic mirror: OpenTofu providers in the OCI provider mirror"
    for provider in hashicorp/vsphere hashicorp/cloudinit; do
        tags "rondo-tofu-local/${provider}" | grep -q '"tags"' || fail "provider ${provider} not in zot"
    done

    step "deploy downstreams: k3d cluster obs, rke2 Target, base layer"
    sandbox deploy downstreams

    step "component install: charts (A), images, machine (B)"
    sandbox component install --timeout 20m

    step "generic mirror: the machine stack's state on the http backend (nginx WebDAV)"
    docker exec rondo-sandbox-mirror sh -c 'find /srv/state -name terraform.tfstate' | grep -q . \
        || fail "no tofu state written through the http backend"
    sandbox logs vm >/dev/null || fail "no Job log for Enrollment vm"
    sandbox ssh vm-app-1 -- test -f /etc/rondo-machine/ca.pem || fail "machine vm-app-1 unreachable"

    step "component install again: nothing changed, verdict at once"
    sandbox component install --timeout 10m

    step "component uninstall"
    sandbox component uninstall acme
    sandbox status
    step "scenario passed"
}

cmd_collect() {
    local dir="${1:?collect <dir>}" kc
    mkdir -p "${dir}"
    sandbox status > "${dir}/status.txt" 2>&1 || true
    sandbox why > "${dir}/why.txt" 2>&1 || true
    cp -R "${STATE}"/*.log "${STATE}"/clusters/*.create.log "${dir}/" 2>/dev/null || true
    for kc in "${STATE}"/clusters/*/kubeconfig.yaml; do
        [[ -f "${kc}" ]] || continue
        local name
        name="$(basename "$(dirname "${kc}")")"
        kubectl --kubeconfig "${kc}" get all,enrollments,components,targets -A -o wide \
            > "${dir}/${name}.objects.txt" 2>&1 || true
        kubectl --kubeconfig "${kc}" get events -A --sort-by=.lastTimestamp > "${dir}/${name}.events.txt" 2>&1 || true
        kubectl --kubeconfig "${kc}" -n rondo-system logs deploy/rondo-controller --tail=2000 \
            > "${dir}/${name}.controller.log" 2>&1 || true
        local job
        for job in $(kubectl --kubeconfig "${kc}" -n rondo-system get jobs -o name 2>/dev/null); do
            kubectl --kubeconfig "${kc}" -n rondo-system logs "${job}" --all-containers \
                > "${dir}/${name}.${job#job.batch/}.log" 2>&1 || true
        done
    done
    local c
    for c in $(docker ps -a --format '{{.Names}}' | grep '^rondo-sandbox\|^rondo-sbx-machine' || true); do
        docker logs --tail 500 "${c}" > "${dir}/docker.${c}.log" 2>&1 || true
    done
}

cmd_cleanup() {
    if [[ -x "${BIN}/rondo-sandbox" && -d "${COMPONENT}" ]]; then
        sandbox destroy --purge || true
    fi
    local id
    for id in $(docker ps -aq --filter name=rondo-sandbox; docker ps -aq --filter name=rondo-sbx); do
        docker rm -f "${id}" >/dev/null 2>&1 || true
    done
    docker network rm rondo-sandbox >/dev/null 2>&1 || true
}

case "${1:-}" in
    tools)   shift; cmd_tools "$@" ;;
    run)     cmd_run ;;
    collect) shift; cmd_collect "$@" ;;
    cleanup) cmd_cleanup ;;
    *) sed -n '2,15p' "$0" >&2; exit 2 ;;
esac
