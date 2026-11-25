#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_FILE="${PROJECT_DIR}/config.env"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
log_ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

die() { log_error "$@"; exit 1; }

# shellcheck source=/dev/null
if [[ -f "${CONFIG_FILE}" ]]; then
    source "${CONFIG_FILE}"
fi

SSH_OPTS=(-o StrictHostKeyChecking=no -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)

export SSHPASS="${SSH_PASSWORD}"

run_ssh() {
    local host="$1"
    shift
    sshpass -e ssh "${SSH_OPTS[@]}" "${SSH_USER}@${host}" "$@"
}

start_k3s_server() {
    local host="$1"
    local role="$2"
    local extra_args=""

    if [[ "${role}" == "init" ]]; then
        extra_args="--cluster-init"
    else
        extra_args="--server https://${VM1_IP}:6443"
    fi

    log_info "Starting k3s on ${host} (role: ${role})..."

    local tmpfile
    tmpfile=$(mktemp)
    cat > "${tmpfile}" <<EOF
[Unit]
Description=Lightweight Kubernetes
Documentation=https://k3s.io
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
EnvironmentFile=-/etc/default/k3s
EnvironmentFile=-/etc/sysconfig/k3s
ExecStart=/usr/local/bin/k3s server \\
    ${extra_args} \\
    --token='${K3S_TOKEN}' \\
    --tls-san=${VM1_IP} \\
    --tls-san=${VM2_IP} \\
    --tls-san=${VM3_IP} \\
    --node-ip=${host} \\
    --advertise-address=${host} \\
    --write-kubeconfig-mode=0644 \\
    --disable=servicelb
KillMode=process
Delegate=yes
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
TimeoutStartSec=0
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF

    log_info "Copying the k3s service to ${host}..."
    sshpass -e scp "${SSH_OPTS[@]}" "${tmpfile}" "${SSH_USER}@${host}:/etc/systemd/system/k3s.service" \
        || { rm -f "${tmpfile}"; die "scp of k3s.service to ${host} failed"; }
    rm -f "${tmpfile}"

    log_info "Enabling the k3s service on ${host}..."
    run_ssh "${host}" "systemctl daemon-reload" \
        || die "daemon-reload failed on ${host}"
    run_ssh "${host}" "systemctl enable --now k3s" \
        || die "enabling k3s failed on ${host}"

    sleep 5
    local status
    status=$(run_ssh "${host}" "systemctl is-active k3s 2>/dev/null" || true)
    if [[ "${status}" != "active" && "${status}" != "activating" ]]; then
        log_error "k3s is not running on ${host} (status: ${status})"
        log_error "--- k3s journalctl on ${host} ---"
        run_ssh "${host}" "journalctl -u k3s --no-pager -n 30" >&2 || true
        die "k3s did not start on ${host}"
    fi

    log_ok "k3s started on ${host} (status: ${status})"
}

wait_for_api() {
    local host="$1"
    local max_attempts=60
    local interval=5
    local attempt=0

    log_info "Waiting for the k3s API server on ${host}..."

    while [[ ${attempt} -lt ${max_attempts} ]]; do
        if run_ssh "${host}" "kubectl get nodes" &>/dev/null; then
            log_ok "API server ready on ${host}"
            return 0
        fi
        attempt=$((attempt + 1))
        sleep "${interval}"
    done

    die "Timeout: API server not ready on ${host} after $((max_attempts * interval))s"
}

wait_for_etcd_quorum() {
    local max_attempts=60
    local interval=5
    local attempt=0

    log_info "Waiting for etcd quorum (3/3 nodes)..."

    while [[ ${attempt} -lt ${max_attempts} ]]; do
        local ready_count
        ready_count=$(run_ssh "${VM1_IP}" "kubectl get nodes --no-headers 2>/dev/null | grep -c ' Ready'" 2>/dev/null || echo "0")

        if [[ "${ready_count}" -ge 3 ]]; then
            log_ok "etcd quorum reached: ${ready_count}/3 nodes Ready"
            return 0
        fi

        log_info "Ready nodes: ${ready_count}/3, waiting..."
        attempt=$((attempt + 1))
        sleep "${interval}"
    done

    die "Timeout: etcd quorum not reached after $((max_attempts * interval))s"
}

fetch_kubeconfig() {
    local kubeconfig_path="${KUBECONFIG_PATH:-${PROJECT_DIR}/kubeconfig.yaml}"

    log_info "Fetching the kubeconfig..."

    mkdir -p "$(dirname "${kubeconfig_path}")"
    sshpass -e scp "${SSH_OPTS[@]}" \
        "${SSH_USER}@${VM1_IP}:/etc/rancher/k3s/k3s.yaml" "${kubeconfig_path}"

    sed -i "s|https://127.0.0.1:6443|https://${VM1_IP}:6443|g" "${kubeconfig_path}"

    export KUBECONFIG="${kubeconfig_path}"
    log_ok "Kubeconfig saved: ${kubeconfig_path}"
}

main() {
    log_info "Forming the HA k3s cluster (3 servers, embedded etcd)"

    start_k3s_server "${VM1_IP}" "init"
    wait_for_api "${VM1_IP}"

    start_k3s_server "${VM2_IP}" "join"
    wait_for_api "${VM2_IP}"

    start_k3s_server "${VM3_IP}" "join"

    wait_for_etcd_quorum

    fetch_kubeconfig

    log_ok "HA k3s cluster formed"
}

main "$@"
