#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Create or update the DNS record that points a hostname at the router, through
# the Cloudflare API, idempotently and only for records this deployment owns.
#
# This is the proxy-TLS companion to the router's own record management
# (server.tls.dns.record): the same ownership-marker rule applies, so a record
# created here is never confused with one somebody else created.
#
# Usage:
#   CF_API_TOKEN=... ZONE=example.com NAME=router.example.com TYPE=A \
#     VALUE=203.0.113.10 ./create-dns-record.sh
#
# Environment:
#   CF_API_TOKEN   scoped Cloudflare token: Zone:Read, DNS:Edit (required)
#   ZONE           zone name, e.g. example.com (required)
#   NAME           record name, e.g. router.example.com (required)
#   TYPE           A, AAAA or CNAME (default A)
#   VALUE          address or CNAME target (required)
#   TTL            record TTL in seconds (default 300)
#   OWNER          owner written to the ownership marker (default: hostname)
#   CF_API_BASE    API base URL (default https://api.cloudflare.com/client/v4;
#                  override only for a local test server)
#
# Exit codes: 0 changed or already correct, 2 usage, 3 record conflict,
#             4 API or transport error.
set -euo pipefail

usage() {
	echo "usage: CF_API_TOKEN=... ZONE=... NAME=... VALUE=... $0" >&2
	exit 2
}

require_env() {
	local name="$1"
	if [ -z "${!name:-}" ]; then
		echo "${name} is required" >&2
		usage
	fi
}
require_env CF_API_TOKEN
require_env ZONE
require_env NAME
require_env VALUE
TYPE="${TYPE:-A}"
TTL="${TTL:-300}"
OWNER="${OWNER:-$(hostname)}"
CF_API_BASE="${CF_API_BASE:-https://api.cloudflare.com/client/v4}"
MARKER_NAME="_metrum-router.${NAME}"
MARKER_VALUE="metrum-ai-router-managed=1;owner=${OWNER};name=${NAME};type=${TYPE}"

case "$TYPE" in
A | AAAA | CNAME) ;;
*) echo "TYPE must be A, AAAA or CNAME" >&2; exit 2 ;;
esac

api() {
	local method="$1" path="$2" body="${3:-}"
	local args=(-sS -X "$method" -H "Authorization: Bearer ${CF_API_TOKEN}" -H "Content-Type: application/json")
	if [ -n "$body" ]; then
		args+=(-d "$body")
	fi
	curl "${args[@]}" "${CF_API_BASE}${path}"
}

require_ok() {
	local response="$1" what="$2"
	if ! printf '%s' "$response" | jq -e '.success == true' >/dev/null 2>&1; then
		echo "provider error while ${what}: $(printf '%s' "$response" | jq -r '.errors // .' 2>/dev/null || printf '%s' "$response")" >&2
		# Never echo the token, even if a shell trace is enabled.
		exit 4
	fi
}

zone_response="$(api GET "/zones?name=${ZONE}")"
require_ok "$zone_response" "looking up zone ${ZONE}"
zone_id="$(printf '%s' "$zone_response" | jq -r '.result[0].id // empty')"
if [ -z "$zone_id" ]; then
	echo "zone ${ZONE} not found for these credentials" >&2
	exit 4
fi

records_response="$(api GET "/zones/${zone_id}/dns_records?type=${TYPE}&name=${NAME}")"
require_ok "$records_response" "listing ${TYPE} ${NAME}"
marker_response="$(api GET "/zones/${zone_id}/dns_records?type=TXT&name=${MARKER_NAME}")"
require_ok "$marker_response" "listing the ownership marker for ${NAME}"

writes=0
existing_id="$(printf '%s' "$records_response" | jq -r '.result[0].id // empty')"
existing_value="$(printf '%s' "$records_response" | jq -r '.result[0].content // empty')"
marker_id="$(printf '%s' "$marker_response" | jq -r '.result[0].id // empty')"
marker_actual="$(printf '%s' "$marker_response" | jq -r '.result[0].content // empty')"

# Parse the marker's fields rather than comparing the whole string: the router's
# own record manager also writes an id field, and either side may have set it.
marker_record_id=""
marker_owner=""
marker_name=""
marker_type=""

marker_field() {
	printf '%s' "$1" | tr ';' '\n' | sed -n "s/^$2=//p" | head -n1
}

if [ -n "$marker_id" ]; then
	marker_owner="$(marker_field "$marker_actual" owner)"
	marker_name="$(marker_field "$marker_actual" name)"
	marker_type="$(marker_field "$marker_actual" type)"
	marker_record_id="$(marker_field "$marker_actual" id)"
	if [ "$marker_owner" != "$OWNER" ] || [ "$marker_name" != "$NAME" ] || [ "$marker_type" != "$TYPE" ]; then
		echo "${TYPE} ${NAME}: the ownership marker names another owner (${marker_actual}); leaving the record untouched" >&2
		exit 3
	fi
	if [ -n "$existing_id" ] && [ -n "$marker_record_id" ] && [ "$marker_record_id" != "$existing_id" ]; then
		echo "${TYPE} ${NAME} was replaced outside this script (record ${existing_id}, marker ${marker_record_id}); leaving it untouched" >&2
		exit 3
	fi
fi
if [ -n "$existing_id" ] && [ -z "$marker_id" ]; then
	echo "${TYPE} ${NAME} already exists without an ownership marker; refusing to modify a record this deployment did not create" >&2
	exit 3
fi

# Claim ownership before touching the record, exactly like the router's record
# manager: a failure between the two writes leaves a marker without an id, which
# the next run adopts instead of conflicting with itself.
if [ -z "$marker_id" ]; then
	claim="$(jq -nc --arg name "$MARKER_NAME" --arg content "$MARKER_VALUE" --argjson ttl "$TTL" \
		'{type: "TXT", name: $name, content: $content, ttl: $ttl}')"
	marker_response="$(api POST "/zones/${zone_id}/dns_records" "$claim")"
	require_ok "$marker_response" "claiming ownership of ${NAME}"
	marker_id="$(printf '%s' "$marker_response" | jq -r '.result.id // empty')"
	writes=$((writes + 1))
	echo "claimed ownership marker ${MARKER_NAME}"
fi

record_id="$existing_id"
if [ -z "$existing_id" ]; then
	body="$(jq -nc --arg type "$TYPE" --arg name "$NAME" --arg content "$VALUE" --argjson ttl "$TTL" \
		'{type: $type, name: $name, content: $content, ttl: $ttl}')"
	create_response="$(api POST "/zones/${zone_id}/dns_records" "$body")"
	require_ok "$create_response" "creating ${TYPE} ${NAME}"
	record_id="$(printf '%s' "$create_response" | jq -r '.result.id // empty')"
	writes=$((writes + 1))
	echo "created ${TYPE} ${NAME} -> ${VALUE}"
elif [ "$existing_value" != "$VALUE" ]; then
	body="$(jq -nc --arg type "$TYPE" --arg name "$NAME" --arg content "$VALUE" --argjson ttl "$TTL" \
		'{type: $type, name: $name, content: $content, ttl: $ttl}')"
	require_ok "$(api PUT "/zones/${zone_id}/dns_records/${existing_id}" "$body")" "updating ${TYPE} ${NAME}"
	writes=$((writes + 1))
	echo "updated ${TYPE} ${NAME} ${existing_value} -> ${VALUE}"
else
	echo "${TYPE} ${NAME} is already ${VALUE}"
fi

# Bind the marker to the record id, like the router's manager does.
if [ -n "$record_id" ] && [ "$marker_record_id" != "$record_id" ]; then
	marker_value="${MARKER_VALUE};id=${record_id}"
	body="$(jq -nc --arg name "$MARKER_NAME" --arg content "$marker_value" --argjson ttl "$TTL" \
		'{type: "TXT", name: $name, content: $content, ttl: $ttl}')"
	require_ok "$(api PUT "/zones/${zone_id}/dns_records/${marker_id}" "$body")" "binding the ownership marker to the record id"
	writes=$((writes + 1))
fi

echo "done (${writes} write call(s))"
