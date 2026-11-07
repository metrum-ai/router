#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Create or update the DNS record that points a hostname at the router, through
# the DigitalOcean (v2) or Cloudflare (v4) API, idempotently and only for
# records this deployment owns.
#
# This is the proxy-TLS companion to the router's own record management
# (server.tls.acme.dns.record): the same ownership-marker rule and format
# apply, so a record created here is never confused with one somebody else
# created.
#
# Usage:
#   PROVIDER=digitalocean DO_AUTH_TOKEN=... ZONE=example.com NAME=router.example.com \
#     TYPE=A VALUE=203.0.113.10 bash ./create-dns-record.sh
#
#   PROVIDER=cloudflare CF_API_TOKEN=... ZONE=example.com NAME=router.example.com \
#     TYPE=A VALUE=203.0.113.10 bash ./create-dns-record.sh
#
# Environment:
#   PROVIDER    digitalocean (default) or cloudflare
#   ZONE        zone name, e.g. example.com (required)
#   NAME        record name, e.g. router.example.com (required)
#   TYPE        A, AAAA or CNAME (default A)
#   VALUE       address or CNAME target (required)
#   TTL         record TTL in seconds (default 300)
#   OWNER       owner written to the ownership marker (default: hostname)
#   DO_AUTH_TOKEN  DigitalOcean token with domain read and record edit rights
#                  (DNS-scoped only); read from the environment, never argv
#   CF_API_TOKEN   Cloudflare token with Zone:Read and DNS:Edit
#   DO_API_BASE / CF_API_BASE
#               API base URL override, for a local test server only
#
# Exit codes: 0 changed or already correct, 2 usage, 3 record conflict,
#             4 API or transport error.
set -euo pipefail

usage() {
	echo "usage: PROVIDER=digitalocean DO_AUTH_TOKEN=... ZONE=... NAME=... VALUE=... $0" >&2
	exit 2
}

require_env() {
	local name="$1"
	if [ -z "${!name:-}" ]; then
		echo "${name} is required" >&2
		usage
	fi
}

PROVIDER="${PROVIDER:-digitalocean}"
PROVIDER="$(printf '%s' "$PROVIDER" | tr '[:upper:]' '[:lower:]')"
case "$PROVIDER" in
digitalocean)
	require_env DO_AUTH_TOKEN
	TOKEN="$DO_AUTH_TOKEN"
	API_BASE="${DO_API_BASE:-https://api.digitalocean.com/v2}"
	;;
cloudflare)
	require_env CF_API_TOKEN
	TOKEN="$CF_API_TOKEN"
	API_BASE="${CF_API_BASE:-https://api.cloudflare.com/client/v4}"
	;;
*)
	echo "PROVIDER must be digitalocean or cloudflare" >&2
	usage
	;;
esac

require_env ZONE
require_env NAME
require_env VALUE
TYPE="${TYPE:-A}"
TTL="${TTL:-300}"
OWNER="${OWNER:-$(hostname)}"
MARKER_NAME="_metrum-router.${NAME}"
MARKER_VALUE="metrum-ai-router-managed=1;owner=${OWNER};name=${NAME};type=${TYPE}"

case "$TYPE" in
A | AAAA | CNAME) ;;
*)
	echo "TYPE must be A, AAAA or CNAME" >&2
	exit 2
	;;
esac

# api METHOD PATH [BODY] prints the response body. The Authorization header is
# written to stdin (curl reads it with -H @-), so the token never appears in the
# process argument list, where any local user could read it.
api() {
	local method="$1" path="$2" body="${3:-}"
	local args=(-sS -X "$method" -H @- -H "Content-Type: application/json")
	if [ -n "$body" ]; then
		args+=(-d "$body")
	fi
	local response status
	response="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl "${args[@]}" -w '\n%{http_code}' "${API_BASE}${path}")"
	status="${response##*$'\n'}"
	response="${response%$'\n'*}"
	if [ "$status" -ge 300 ] 2>/dev/null; then
		echo "provider error ${status} for ${method} ${path}: $(printf '%s' "$response" | head -c 512)" >&2
		exit 4
	fi
	printf '%s' "$response"
}

# name_relative strips the zone suffix for providers whose API takes names
# relative to the zone.
name_relative() {
	local name="$1"
	if [ "$name" = "$ZONE" ]; then
		printf '@'
		return
	fi
	printf '%s' "${name%.$ZONE}"
}

# name_absolute is the inverse of name_relative.
name_absolute() {
	local name="$1"
	case "$name" in
	'' | '@') printf '%s' "$ZONE" ;;
	*".$ZONE") printf '%s' "$name" ;;
	*) printf '%s.%s' "$name" "$ZONE" ;;
	esac
}

# zone_ref resolves the provider's zone handle (Cloudflare id, DigitalOcean
# zone name).
zone_ref() {
	case "$PROVIDER" in
	digitalocean)
		api GET "/domains/${ZONE}" >/dev/null
		printf '%s' "$ZONE"
		;;
	cloudflare)
		local zones id
		zones="$(api GET "/zones?name=${ZONE}")"
		id="$(printf '%s' "$zones" | jq -r '.result[]? | select(.name == "'"$ZONE"'") | .id' | head -n1)"
		if [ -z "$id" ]; then
			echo "zone ${ZONE} not found for these credentials" >&2
			exit 4
		fi
		printf '%s' "$id"
		;;
	esac
}

# list_records TYPE NAME prints a JSON array of {id,name,value} for records of
# that type whose absolute name matches. Names are absolute; values are
# unquoted.
list_records() {
	local type="$1" name="$2" zone_id
	zone_id="$(zone_ref)"
	case "$PROVIDER" in
	digitalocean)
		api GET "/domains/${ZONE}/records?per_page=200" | jq -c --arg type "$type" --arg name "$(name_relative "$name")" '
			[.domain_records[]? | select(.type == $type and .name == $name)
			 | {id: (.id | tostring), name: .name, value: (.data | tostring)}]'
		;;
	cloudflare)
		api GET "/zones/${zone_id}/dns_records?per_page=100&type=${type}&name=${name}" | jq -c '
			[.result[]? | {id: .id, name: .name, value: (.content | tostring)}]'
		;;
	esac
}

# create_record TYPE NAME VALUE TTL prints the new record id.
create_record() {
	local type="$1" name="$2" value="$3" ttl="$4" zone_id response
	zone_id="$(zone_ref)"
	case "$PROVIDER" in
	digitalocean)
		response="$(api POST "/domains/${ZONE}/records" "$(jq -nc --arg type "$type" --arg name "$(name_relative "$name")" --arg data "$value" --argjson ttl "$ttl" \
			'{type: $type, name: $name, data: $data, ttl: $ttl}')")"
		printf '%s' "$response" | jq -r '.domain_record.id // empty'
		;;
	cloudflare)
		response="$(api POST "/zones/${zone_id}/dns_records" "$(jq -nc --arg type "$type" --arg name "$name" --arg content "$value" --argjson ttl "$ttl" \
			'{type: $type, name: $name, content: $content, ttl: $ttl}')")"
		printf '%s' "$response" | jq -r 'if .success then (.result.id // empty) else empty end'
		;;
	esac
}

# update_record ID TYPE NAME VALUE TTL
update_record() {
	local id="$1" type="$2" name="$3" value="$4" ttl="$5" zone_id
	zone_id="$(zone_ref)"
	case "$PROVIDER" in
	digitalocean)
		api PUT "/domains/${ZONE}/records/${id}" "$(jq -nc --arg type "$type" --arg name "$(name_relative "$name")" --arg data "$value" --argjson ttl "$ttl" \
			'{type: $type, name: $name, data: $data, ttl: $ttl}')" >/dev/null
		;;
	cloudflare)
		api PUT "/zones/${zone_id}/dns_records/${id}" "$(jq -nc --arg type "$type" --arg name "$name" --arg content "$value" --argjson ttl "$ttl" \
			'{type: $type, name: $name, content: $content, ttl: $ttl}')" >/dev/null
		;;
	esac
}

# marker_field reads one field out of an ownership marker value.
marker_field() {
	local value="$1" key="$2"
	printf '%s' "$value" | tr ';' '\n' | sed -n "s/^${key}=//p" | head -n1
}

existing_json="$(list_records "$TYPE" "$NAME")"
marker_json="$(list_records TXT "$MARKER_NAME")"
existing_id="$(printf '%s' "$existing_json" | jq -r '.[0].id // empty')"
existing_value="$(printf '%s' "$existing_json" | jq -r '.[0].value // empty')"
marker_id="$(printf '%s' "$marker_json" | jq -r '.[0].id // empty')"
marker_actual="$(printf '%s' "$marker_json" | jq -r '.[0].value // empty')"

marker_record_id=""
marker_owner=""
marker_name=""
marker_type=""
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

writes=0

# Claim ownership before touching the record, exactly like the router's record
# manager: a failure between the two writes leaves a marker without an id, which
# the next run adopts instead of conflicting with itself.
if [ -z "$marker_id" ]; then
	marker_id="$(create_record TXT "$MARKER_NAME" "$MARKER_VALUE" "$TTL")"
	if [ -z "$marker_id" ]; then
		echo "could not claim the ownership marker ${MARKER_NAME}" >&2
		exit 4
	fi
	writes=$((writes + 1))
	echo "claimed ownership marker ${MARKER_NAME}"
fi

record_id="$existing_id"
if [ -z "$existing_id" ]; then
	record_id="$(create_record "$TYPE" "$NAME" "$VALUE" "$TTL")"
	if [ -z "$record_id" ]; then
		echo "could not create ${TYPE} ${NAME}" >&2
		exit 4
	fi
	writes=$((writes + 1))
	echo "created ${TYPE} ${NAME} -> ${VALUE}"
elif [ "$existing_value" != "$VALUE" ]; then
	update_record "$existing_id" "$TYPE" "$NAME" "$VALUE" "$TTL"
	writes=$((writes + 1))
	echo "updated ${TYPE} ${NAME} ${existing_value} -> ${VALUE}"
else
	echo "${TYPE} ${NAME} is already ${VALUE}"
fi

# Bind the marker to the record id, like the router's manager does.
if [ -n "$record_id" ] && [ "$marker_record_id" != "$record_id" ]; then
	update_record "$marker_id" TXT "$MARKER_NAME" "${MARKER_VALUE};id=${record_id}" "$TTL"
	writes=$((writes + 1))
fi

echo "done (${writes} write call(s))"
