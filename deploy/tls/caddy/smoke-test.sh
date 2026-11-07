#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Documented smoke test for the Caddy proxy-TLS recipe (deploy/tls/caddy).
#
# It validates the Caddyfile, starts Caddy against the chosen ACME directory,
# waits for the certificate to be issued over DNS-01, prints the issuer and
# expiry, and stops Caddy again. Because it needs real DNS provider credentials
# and a hostname whose zone the operator controls, it runs on an operator
# machine rather than in CI (the CI runners have no container runtime and no
# DNS credentials).
#
# Usage:
#   ROUTER_DNS_PROVIDER=digitalocean \
#   ROUTER_HOSTNAME=router-test.example.com \
#   ROUTER_ACME_EMAIL=ops@example.com \
#   DO_AUTH_TOKEN=... \
#     bash ./smoke-test.sh /path/to/caddy
#
# Environment:
#   ROUTER_DNS_PROVIDER   digitalocean (default) or cloudflare; picks the
#                         Caddyfile variant and the credential to require
#   ROUTER_HOSTNAME       hostname to certify (required, must resolve already)
#   ROUTER_ACME_EMAIL     ACME account contact (required)
#   DO_AUTH_TOKEN         DigitalOcean DNS token (required for digitalocean)
#   CF_DNS_API_TOKEN      Cloudflare token (required for cloudflare)
#   ROUTER_ACME_CA        ACME directory; use the Let's Encrypt staging directory
#                         for a non-production run
#   ROUTER_ACME_CA_ROOT   PEM bundle that signs the ACME directory certificate
#                         (default: the system trust bundle; a local test CA sets
#                         this to its own root)
#   CADDY_DATA_DIR        Caddy state directory (default: a temporary directory)
#   ROUTER_SERVE_ADDR     serving listener (default 127.0.0.1:8080)
#   ROUTER_ADMIN_ADDR     admin listener (default 127.0.0.1:8081)
#   SMOKE_TIMEOUT_SECONDS how long to wait for the certificate (default 180)
set -euo pipefail

CADDY_BIN="${1:-caddy}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
: "${ROUTER_HOSTNAME:?ROUTER_HOSTNAME is required}"
: "${ROUTER_ACME_EMAIL:?ROUTER_ACME_EMAIL is required}"
ROUTER_DNS_PROVIDER="${ROUTER_DNS_PROVIDER:-digitalocean}"
SMOKE_TIMEOUT_SECONDS="${SMOKE_TIMEOUT_SECONDS:-180}"

case "$ROUTER_DNS_PROVIDER" in
digitalocean)
	: "${DO_AUTH_TOKEN:?DO_AUTH_TOKEN is required for ROUTER_DNS_PROVIDER=digitalocean}"
	CONFIG="$HERE/Caddyfile"
	MODULE="dns.providers.digitalocean"
	;;
cloudflare)
	: "${CF_DNS_API_TOKEN:?CF_DNS_API_TOKEN is required for ROUTER_DNS_PROVIDER=cloudflare}"
	CONFIG="$HERE/Caddyfile.cloudflare"
	MODULE="dns.providers.cloudflare"
	;;
*)
	echo "ROUTER_DNS_PROVIDER must be digitalocean or cloudflare" >&2
	exit 2
	;;
esac

if ! command -v "$CADDY_BIN" >/dev/null 2>&1; then
	echo "Caddy binary '$CADDY_BIN' not found; build it with xcaddy and the $MODULE module (see README.md)" >&2
	exit 2
fi
if ! "$CADDY_BIN" list-modules 2>/dev/null | grep -qx "$MODULE"; then
	echo "this Caddy has no $MODULE module; rebuild it with:" >&2
	echo "  xcaddy build --with github.com/caddy-dns/$ROUTER_DNS_PROVIDER" >&2
	exit 2
fi

export ROUTER_HOSTNAME ROUTER_ACME_EMAIL CADDY_DATA_DIR
export ROUTER_SERVE_ADDR="${ROUTER_SERVE_ADDR:-127.0.0.1:8080}"
export ROUTER_ADMIN_ADDR="${ROUTER_ADMIN_ADDR:-127.0.0.1:8081}"
if [ -z "${CADDY_DATA_DIR:-}" ]; then
	CADDY_DATA_DIR="$(mktemp -d)"
	export CADDY_DATA_DIR
	CLEAN_DATA_DIR=1
fi
LOG_FILE="$(mktemp)"
trap 'if [ -n "${CADDY_PID:-}" ]; then kill "$CADDY_PID" 2>/dev/null || true; wait "$CADDY_PID" 2>/dev/null || true; fi; if [ -n "${CLEAN_DATA_DIR:-}" ]; then rm -rf "$CADDY_DATA_DIR"; fi' EXIT

echo "validating $CONFIG"
"$CADDY_BIN" validate --config "$CONFIG" --adapter caddyfile

echo "starting Caddy for ${ROUTER_HOSTNAME} (${ROUTER_DNS_PROVIDER})"
"$CADDY_BIN" run --config "$CONFIG" --adapter caddyfile >"$LOG_FILE" 2>&1 &
CADDY_PID=$!

deadline=$((SECONDS + SMOKE_TIMEOUT_SECONDS))
certificate=""
while [ "$SECONDS" -lt "$deadline" ]; do
	if ! kill -0 "$CADDY_PID" 2>/dev/null; then
		echo "Caddy exited before a certificate was issued:" >&2
		cat "$LOG_FILE" >&2
		exit 1
	fi
	certificate="$(find "$CADDY_DATA_DIR" -name '*.crt' -path '*certificates*' -print -quit 2>/dev/null || true)"
	if [ -n "$certificate" ]; then
		break
	fi
	if grep -q 'error' "$LOG_FILE"; then
		echo "Caddy reported an error while obtaining the certificate:" >&2
		grep 'error' "$LOG_FILE" >&2 || true
	fi
	sleep 2
done

if [ -z "$certificate" ]; then
	echo "no certificate was issued within ${SMOKE_TIMEOUT_SECONDS}s" >&2
	tail -n 50 "$LOG_FILE" >&2
	exit 1
fi

echo "certificate issued over DNS-01:"
openssl x509 -in "$certificate" -noout -subject -issuer -dates
echo
echo "Next: request https://${ROUTER_HOSTNAME}/healthz through Caddy to confirm the proxy path."
echo "smoke test passed"
