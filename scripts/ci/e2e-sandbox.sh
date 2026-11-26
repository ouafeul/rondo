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
KUBECTL_VERSION="v1.36.6"
KUBECTL_SHA256="29818b6729dc9262b692307470d0558d027725e738be5dd47c760261868a2634"
HELM_VERSION="v4.3.2"
HELM_SHA256="d1c0278d46beab79582d7e50f5b3dc206944ba8e577cde618513fd07daa53983"

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
    docker ps -a --format '{{.Names}}\t{{.Status}}\t{{.Networks}}' > "${dir}/docker.ps.txt" 2>&1 || true
    # Machines: is systemd up, is sshd listening, and can the network reach port 22?
    local m ip
    for m in $(docker ps -a --format '{{.Names}}' | grep '^rondo-sbx-machine' || true); do
        ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${m}" 2>/dev/null)"
        {
            docker inspect -f 'state={{.State.Status}} restarts={{.RestartCount}} exit={{.State.ExitCode}} err={{.State.Error}}' "${m}"
            docker exec "${m}" sh -c 'grep -E "^Cap(Eff|Bnd)" /proc/1/status; ls -l /etc/shadow'
            docker info --format 'security={{.SecurityOptions}} cgroup={{.CgroupVersion}}'
            docker exec "${m}" systemctl is-system-running
            docker exec "${m}" systemctl --no-pager --failed
            docker exec "${m}" systemctl --no-pager status sshd
            docker exec "${m}" sh -c 'ss -ltnp 2>/dev/null || netstat -ltnp'
            docker exec "${m}" ls -l /var/lib/rondo-cloud-init-done /home/rondo/.ssh/authorized_keys
            docker exec "${m}" journalctl --no-pager -n 100
            echo "--- probe ${ip}:22 from the sandbox network"
            docker run --rm --network rondo-sandbox busybox:1.38.0 nc -zv -w 5 "${ip}" 22
            local node
            for node in $(docker ps --format '{{.Names}}' | grep '^k3d-.*server-0$' || true); do
                echo "--- probe ${ip}:22 from ${node}"
                docker exec "${node}" sh -c "nc -zv -w 5 ${ip} 22 || wget -T 5 -O- http://${ip}:22"
            done
        } > "${dir}/machine.${m}.txt" 2>&1 || true
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
