#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl python3 python3-venv gnupg
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod 0644 /etc/apt/keyrings/docker.asc
. /etc/os-release
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu %s stable\n' "$(dpkg --print-architecture)" "$VERSION_CODENAME" > /etc/apt/sources.list.d/docker.list
apt-get update -qq
apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
docker version >/dev/null
docker compose version
python3 -m venv /root/harbor-venv
HARBOR_VERSION="${HARBOR_VERSION:-0.25.0}"
if ! printf '%s' "$HARBOR_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then echo "unpinned HARBOR_VERSION" >&2; exit 2; fi
/root/harbor-venv/bin/pip install -q "harbor==${HARBOR_VERSION}"
install -d -m 700 /run/harbor-e2e /root/harbor-e2e/jobs
[ "$(findmnt -no FSTYPE --target /run/harbor-e2e)" = tmpfs ]
printf 'Remote setup complete; credentials not yet installed\n'
