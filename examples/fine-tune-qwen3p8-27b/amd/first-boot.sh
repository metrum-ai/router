#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Run as root on the owned droplet BEFORE pulling/loading models.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root on the droplet' >&2; exit 2; }
hook=/var/lib/cloud/scripts/per-instance/001_onboot
if [[ -f "$hook" ]]; then
  mv "$hook" "$hook.amd359-disabled"
fi
# Marketplace rocm may expose Jupyter/vLLM. Stop regardless of current bindings.
if docker container inspect rocm >/dev/null 2>&1; then
  docker update --restart=no rocm >/dev/null
  docker stop rocm >/dev/null
fi
# Caddy can independently publish a Jupyter reverse proxy.
if command -v systemctl >/dev/null && systemctl is-active --quiet caddy; then
  systemctl disable --now caddy
fi
python3 - <<'PY'
import ipaddress
import subprocess
rows = subprocess.check_output(['ss', '-H', '-lntup'], text=True)
unsafe = []
for row in rows.splitlines():
    fields = row.split()
    endpoint = fields[4]
    host, port = endpoint.rsplit(':', 1)
    host = host.strip('[]').split('%')[0]
    try:
        loopback = ipaddress.ip_address(host).is_loopback
    except ValueError:
        loopback = False
    # SSH is the only allowed public listener, TCP only.
    if not loopback and not (fields[0] == 'tcp' and port == '22'):
        unsafe.append(endpoint)
if unsafe:
    raise SystemExit('FAIL: non-SSH public listeners: ' + ', '.join(unsafe))
print('PASS: no non-SSH public listeners (TCP/UDP)')
PY
