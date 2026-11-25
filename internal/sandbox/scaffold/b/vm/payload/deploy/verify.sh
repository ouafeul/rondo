#!/bin/bash
# verify phase: the page answers, or the Enrollment fails with the unit's status.
set -euo pipefail
for _ in $(seq 1 20); do
    curl -fsS http://127.0.0.1:8080/ >/dev/null && exit 0
    sleep 1
done
echo "__NAME__.service does not answer" >&2
systemctl status __NAME__.service --no-pager >&2 || true
exit 1
