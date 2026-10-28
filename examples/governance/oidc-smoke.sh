#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
cd "$(dirname "$0")/../.."
# Real router handler and synthetic loopback OIDC issuer, no real identity provider.
go test ./internal/router -run '^TestAdminOIDC(LoginCallbackMeAndLogout|SessionAuthorizesReportsWithCasbin)$' -count=1 -timeout=2m
