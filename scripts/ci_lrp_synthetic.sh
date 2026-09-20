#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

# Credential-free Learned Routing Policy sandbox used by pull-request path
# filters and the merge-group deterministic gate. Does not call providers.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

uv sync --project services/learned-routing-policy --locked

# Only Ubuntu supplies these prerequisites. Unrelated preinstalled
# browser/package feeds must not break verifier setup during updates.
test -f /etc/apt/sources.list.d/ubuntu.sources
sudo mkdir -p /var/tmp/lrp-ci-apt-sources
sudo cp /etc/apt/sources.list.d/ubuntu.sources /var/tmp/lrp-ci-apt-sources/ubuntu.sources
apt_sources=(-o Dir::Etc::sourcelist=- -o Dir::Etc::sourceparts=/var/tmp/lrp-ci-apt-sources)
sudo apt-get "${apt_sources[@]}" update
# Bubblewrap namespaces are the isolation boundary. AppArmor profiles are
# not required — Docker-based self-hosted pools cannot load host policy
# (`apparmor_parser` fails with "interface file missing").
sudo apt-get "${apt_sources[@]}" install -y bubblewrap
umask 077
mkdir -p /var/tmp/lrp-ci
# Self-hosted compose runners may reuse /var/tmp across jobs.
rm -rf /var/tmp/lrp-ci/rootfs
cp services/learned-routing-policy/verifier-requirements.lock /var/tmp/lrp-ci/requirements.lock
bash services/learned-routing-policy/lrp/judge/build_rootfs.sh \
  --base-image python@sha256:78387bc3881b8273120a12ebe6c1ab22b018ccc2c9adf565ae1ac9b536e184ea \
  --requirements-lock /var/tmp/lrp-ci/requirements.lock \
  --out /var/tmp/lrp-ci/rootfs

bwrap --version
for name in kernel/apparmor_restrict_unprivileged_userns kernel/unprivileged_userns_clone user/max_user_namespaces; do
  if test -f "/proc/sys/$name"; then printf '%s=' "$name"; cat "/proc/sys/$name"; fi
done
uv run --project services/learned-routing-policy python - <<'PY'
import json
import subprocess
from pathlib import Path
from lrp.judge.sandbox import Sandbox
command = Sandbox(Path('/var/tmp/lrp-ci/rootfs')).command()
result = subprocess.run(command, input='{"kind":"exact","spec":{"expected":"4"},"content":"4"}\n', text=True, capture_output=True, timeout=35, env={})
classes = {name: token in result.stderr.lower() for name, token in {
    'namespace_denied': 'creating new namespace failed',
    'permission_denied': 'permission denied',
    'operation_not_permitted': 'operation not permitted',
    'uid_map_error': 'uid_map',
    'missing_runtime': 'no such file',
    'unknown_option': 'unknown option',
}.items()}
print(json.dumps({'sandbox_preflight_exit': result.returncode, 'diagnostics': classes}))
assert result.returncode == 0 and json.loads(result.stdout) == {'passed': True}, 'sandbox_preflight_failed'
PY

# The rootfs build above uses umask 077. Tests create a 0o755 directory and
# expect it to stay public; leaving 077 on turns that into 0o700.
umask 022
export LRP_TEST_ROOTFS=/var/tmp/lrp-ci/rootfs
export LRP_REQUIRE_SANDBOX_TESTS=1
make lrp-test lrp-synthetic-demo lrp-e2e LRP_JUNIT_REPORT=/var/tmp/lrp-ci/tests.xml

python3 - <<'PY'
import json
import os
from pathlib import Path
from xml.etree import ElementTree
os.umask(0o077)
suites = ElementTree.fromstring(Path('/var/tmp/lrp-ci/tests.xml').read_bytes())
totals = {key: sum(int(suite.attrib[key]) for suite in suites) for key in ('tests', 'errors', 'failures', 'skipped')}
assert totals['tests'] > 0 and not any(totals[key] for key in ('errors', 'failures', 'skipped'))
summary = {'schema_version': 'lrp.public-tests.v1', 'source': 'synthetic', 'real_verifier_isolation_required': True, 'result': 'passed', **totals}
Path('/var/tmp/metrum-lrp-synthetic-demo/public-tests.json').write_text(json.dumps(summary, indent=2) + '\n')
PY

go test ./internal/router -run 'ExternalRoutingPolicy|Config'
python3 scripts/check_env_example_secrets.py
for name in public-training.json public-training.log public-inference.json public-tests.json; do
  test -s "/var/tmp/metrum-lrp-synthetic-demo/$name"
done
