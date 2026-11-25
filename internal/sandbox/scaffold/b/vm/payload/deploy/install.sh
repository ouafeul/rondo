#!/bin/bash
# install phase, run with sudo: the page with the Target message, served by a systemd unit.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=/dev/null
. ./app.conf
# shellcheck source=/dev/null
. ./secrets.env
mkdir -p /opt/__NAME__/www
cat > /opt/__NAME__/www/index.html <<EOF
<!doctype html>
<title>__NAME__</title>
<h1>${MESSAGE}</h1>
<p>$(cat /proc/sys/kernel/hostname): token of ${#TOKEN} characters</p>
EOF
cat > /etc/systemd/system/__NAME__.service <<UNIT
[Unit]
Description=__NAME__
[Service]
ExecStart=/usr/bin/python3 -m http.server 8080 --directory /opt/__NAME__/www
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now __NAME__.service
