VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS ?= -X github.com/metrum-ai/router/internal/buildinfo.Version=$(VERSION) -X github.com/metrum-ai/router/internal/buildinfo.Commit=$(COMMIT) -X github.com/metrum-ai/router/internal/buildinfo.BuildDate=$(BUILD_DATE)
DIST_DIR ?= dist
PKG_NAME ?= metrum-ai-router
GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)
HOST_GOOS := $(shell go env GOHOSTOS)
HOST_GOARCH := $(shell go env GOHOSTARCH)
PYTHON ?= python3
# Packaged CLIs are ELF binaries only. Release packages never ship Go source,
# cmd/, internal/, or go.mod. Packages ship canonical metrum-ai-router* binaries
# only.
PACKAGE_BINARIES := metrum-ai-router metrum-ai-router-token-gen metrum-ai-router-usage-report metrum-ai-router-migrate metrum-ai-routerctl
DOCKER_RUNTIME_BINARIES := metrum-ai-router metrum-ai-router-token-gen metrum-ai-router-usage-report metrum-ai-router-migrate metrum-ai-routerctl


# Inspect coding evaluations are deliberately opt-in: they call a live endpoint
# and may start Docker sandboxes.  They are never prerequisites of test/build.
EVAL_MODEL ?=
EVAL_BASE_URL ?=
EVAL_API ?= openai
EVAL_LIMIT ?= 8
EVAL_CONCURRENCY ?= 1
EVAL_TIMEOUT ?= 300
# Each Make process receives one isolated run directory, shared by explicitly
# requested suite/report targets in that same invocation. CI pins EVAL_RUN_ID
# across separate Make calls in one workflow run.
EVAL_LOG_ROOT ?= tmp/inspect-evals
ifeq ($(origin EVAL_RUN_ID), undefined)
EVAL_RUN_ID := $(shell date -u +%Y%m%dT%H%M%SZ)-$(shell printf '%s' $$$$)
endif
EVAL_LOG_DIR ?= $(EVAL_LOG_ROOT)/$(EVAL_RUN_ID)
EVAL_REASONING ?=
# Ordinary bounded evaluations leave this false.  Set it to true only for the
# protected reasoning-coverage path, which requires exactly one usage config
# source and a dedicated caller identity.
EVAL_REQUIRE_REASONING_COVERAGE ?= false
# Path to the aggregate-only reasoning coverage JSON exported from the protected
# usage DB; never point this at raw request or Inspect logs.
EVAL_REASONING_COVERAGE_FILE ?=
EVAL_USAGE_CONFIG_YAML ?=
EVAL_USAGE_CONFIG_FILE ?=
EVAL_USAGE_CALLER_ID ?=
EVAL_USAGE_CLIENT ?=
EVAL_USAGE_REPORT_BIN ?= go
EVAL_USAGE_EXPORT_TIMEOUT ?= 60
EVAL_MODEL_KIND ?= router-group
EVAL_SUITE ?= humaneval
EVAL_POLICY ?= config/evaluation-policy.example.json
EVAL_INSPECT ?= inspect
EVAL_CI_REPORT_DIR ?= docs/evaluation-reports/inspect
EVAL_CI_REPORT_TIMESTAMP ?=
EVAL_SAVE_CI_REPORT ?= false
export EVAL_MODEL EVAL_BASE_URL EVAL_API EVAL_LIMIT EVAL_CONCURRENCY EVAL_TIMEOUT EVAL_LOG_ROOT EVAL_RUN_ID EVAL_LOG_DIR EVAL_REASONING EVAL_REQUIRE_REASONING_COVERAGE EVAL_REASONING_COVERAGE_FILE EVAL_USAGE_CONFIG_YAML EVAL_USAGE_CONFIG_FILE EVAL_USAGE_CALLER_ID EVAL_USAGE_CLIENT EVAL_USAGE_REPORT_BIN EVAL_USAGE_EXPORT_TIMEOUT EVAL_MODEL_KIND EVAL_SUITE EVAL_BASELINE_JSON EVAL_POLICY EVAL_INSPECT EVAL_CI_REPORT_DIR EVAL_CI_REPORT_TIMESTAMP EVAL_SAVE_CI_REPORT

# Keep the repository's historical validation contract for bare `make` even
# though the EKS help target appears earlier in this file.
.DEFAULT_GOAL := test

# Tenant discovery/network-policy helpers (not staging delivery).
DOCKER ?= docker

DOCKER_BUILDX ?= $(DOCKER) buildx
DOCKER_PLATFORM ?= linux/$(GOARCH)
# Unqualified local tag used by make docker / docker save. Published images are
# also tagged as ghcr.io/metrum-ai/router (same IMAGE_TAG suffix).
IMAGE_NAME ?= metrum-ai-router
IMAGE_TAG ?= $(VERSION)-$(GOOS)-$(GOARCH)
DOCS_SITE_DIR ?= docs-site
DOCS_EMBED_DIR ?= internal/router/docsdist
PACKAGE_DOC_ALLOWLIST ?= scripts/package_docs_allowlist.txt
EKS_AWS_PROFILE ?=
EKS_ACCOUNT_ID ?=
EKS_REGION ?=
EKS_NAMESPACE ?=
EKS_LINKERD_NAMESPACE ?=
EKS_INGRESS_NAMESPACE ?=
EKS_INGRESS_SERVICE_ACCOUNT ?=
EKS_INGRESS_DEPLOYMENT ?=
EKS_LINKERD_TRUST_DOMAIN ?=
EKS_ECR_REPOSITORY ?=
EKS_DISCOVERY_OUTPUT ?=
EKS_LINKERD_POLICY_OUTPUT ?=
EKS_INGRESS_NETWORK_POLICY_OUTPUT ?=
EKS_POLICY_AWS_PROFILE ?=
EKS_POLICY_KUBECONFIG ?=
EKS_POLICY_CONTEXT ?=
EKS_POLICY_APPLY_CONFIRM ?=
EKS_ADMIN_PROFILE ?= default
EKS_SOURCE_USER ?= smartrouter
EKS_MFA_SERIAL ?=
EKS_MFA_KEYCHAIN_SERVICE ?=
EKS_MFA_KEYCHAIN_ACCOUNT ?= smartrouter
EKS_SESSION_DURATION ?= 3600
COPYFILE_DISABLE ?= 1
export VERSION COMMIT BUILD_DATE DIST_DIR PKG_NAME GOOS GOARCH IMAGE_NAME IMAGE_TAG PYTHON AWS_REGION EKS_CLUSTER K8S_NAMESPACE KUSTOMIZE_OVERLAY ENVIRONMENT EKS_AWS_PROFILE EKS_ACCOUNT_ID EKS_REGION EKS_NAMESPACE EKS_LINKERD_NAMESPACE EKS_INGRESS_NAMESPACE EKS_INGRESS_SERVICE_ACCOUNT EKS_INGRESS_DEPLOYMENT EKS_LINKERD_TRUST_DOMAIN EKS_ECR_REPOSITORY EKS_DISCOVERY_OUTPUT EKS_LINKERD_POLICY_OUTPUT EKS_INGRESS_NETWORK_POLICY_OUTPUT EKS_POLICY_AWS_PROFILE EKS_POLICY_KUBECONFIG EKS_POLICY_CONTEXT EKS_POLICY_APPLY_CONFIRM EKS_ADMIN_PROFILE EKS_SOURCE_USER EKS_MFA_SERIAL EKS_MFA_KEYCHAIN_SERVICE EKS_MFA_KEYCHAIN_ACCOUNT EKS_SESSION_DURATION
export COPYFILE_DISABLE
TAR_ENV := COPYFILE_DISABLE=1

BUILD_LDFLAGS = -X github.com/metrum-ai/router/internal/buildinfo.Version=$${VERSION} -X github.com/metrum-ai/router/internal/buildinfo.Commit=$${COMMIT} -X github.com/metrum-ai/router/internal/buildinfo.BuildDate=$${BUILD_DATE}

.PHONY: help test test-fast test-full secret-contract capability-smoke-contracts test-k8s-nvidia-local-serving test-k8s-amd-instinct-local-serving test-migration-operational-postgres test-migration-data-jobs-postgres test-migration-data-job-ownership-postgres test-reasoning-telemetry-postgres test-usage-schema-postgres-indexes capability-smoke capability-smoke-unit capability-smoke-live api-compat-bootstrap api-compat-bootstrap-go-provision api-compat-mock api-compat-mock-offline api-compat-live harbor-local harbor-local-offline harbor-adapter-test outcome-calibrated-demo outcome-calibrated-synthetic-demo adaptive-signal-policy-demo openjev-routing-demo secret-check validate-build-metadata validate-release-clean release-validation-matrix release-artifact-inventory release-security-evidence launch-operational-readiness release-notes-from-git docs-diag-schema docs-diag-schema-check docs-qa docs-build docs-dev docs-clean admin-build admin-e2e build build-go-only build-package-binaries build-all package package-one package-one-no-docs package-all docker-image docker-image-no-docs package-docker package-docker-one package-docker-one-no-docs package-docker-all dist-backup package-dist-backup compose-security-check eks-session-bootstrap eks-session-recovery-status eks-identity-check eks-discovery-validate eks-discover eks-render-ingress-network-policy eks-validate-tenant-network-policies eks-apply-tenant-network-policies e2e-mock e2e-live-c e2e-live-full e2e-compose-live eval-humaneval eval-bigcodebench eval-report eval-ci-smoke eval-ci-full livecodebench-contract-test livecodebench-target-test livecodebench-validate livecodebench-run proof-routing sse-capture clean

help:
	@echo "Metrum AI Router make targets."
	@echo "  test                   run full credential-free suite"
	@echo "  test-fast              pull-request checks without uv suites"
	@echo "  test-full              same contract as test, for the merge queue"
	@echo "  proof-routing          same-group different-upstream dynamic_score proof"
	@echo "  harbor-adapter-test    offline Harbor agent-adapter contracts (AGENT-01..06)"
	@echo "  package-docker         build customer Docker packages"
	@echo "  docs-build             build embedded public docs"
	@echo "  sse-capture            generate synthetic SSE fixtures and replay goldens"

test-reasoning-telemetry-postgres:
	bash scripts/test_reasoning_telemetry_postgres.sh

test-migration-operational-postgres:
	bash scripts/test_migration_operational_postgres.sh

test-migration-data-jobs-postgres:
	bash scripts/test_migration_data_jobs_postgres.sh

test-migration-data-job-ownership-postgres:
	bash scripts/test_migration_data_job_ownership_postgres.sh

test-usage-schema-postgres-indexes:
	bash scripts/test_usage_schema_postgres_indexes.sh

eval-humaneval:
	$(PYTHON) scripts/inspect_coding_eval.py run --suite humaneval

eval-bigcodebench:
	$(PYTHON) scripts/inspect_coding_eval.py run --suite bigcodebench

eval-report:
	$(PYTHON) scripts/inspect_coding_eval.py report --log-dir "$${EVAL_LOG_DIR}" --suite "$${EVAL_SUITE}" --policy "$${EVAL_POLICY}"

# A small, authenticated router-group check for explicitly enabled CI only.
eval-ci-smoke:
	$(PYTHON) scripts/inspect_coding_eval.py smoke

# Full CI sequencing is implemented in the reusable wrapper, not in a CI YAML
# shell block. It requires protected per-suite baselines and remains opt-in.
eval-ci-full:
	$(PYTHON) scripts/inspect_coding_eval.py ci-full

# LiveCodeBench is opt-in. It needs an evaluator checkout created from the
# pinned contract and can download the official public dataset; it is never a
# prerequisite of make/test/build.
LCB_ROOT ?=
LCB_PYTHON ?= $(PYTHON)
LCB_RUNNER_COMMAND_FILE ?=
LCB_ROOT_ABS := $(abspath $(LCB_ROOT))
LCB_RUNNER_COMMAND_FILE_ABS := $(abspath $(LCB_RUNNER_COMMAND_FILE))
livecodebench-contract-test: livecodebench-target-test
	$(PYTHON) scripts/livecodebench_eval_test.py

livecodebench-target-test:
	$(PYTHON) scripts/livecodebench_make_targets_test.py

livecodebench-validate:
	@test -n "$(LCB_ROOT)" || { echo "LCB_ROOT must name the pinned LiveCodeBench checkout" >&2; exit 2; }
	cd "$(LCB_ROOT_ABS)" && PYTHONPATH="$(LCB_ROOT_ABS)$${PYTHONPATH:+:$${PYTHONPATH}}" $(LCB_PYTHON) "$(CURDIR)/scripts/livecodebench_eval.py" validate --lcb-root "$(LCB_ROOT_ABS)"

livecodebench-run:
	@test -n "$(LCB_ROOT)" && test -n "$(LCB_RUNNER_COMMAND_FILE)" || { echo "LCB_ROOT and LCB_RUNNER_COMMAND_FILE are required" >&2; exit 2; }
	cd "$(LCB_ROOT_ABS)" && PYTHONPATH="$(LCB_ROOT_ABS)$${PYTHONPATH:+:$${PYTHONPATH}}" $(LCB_PYTHON) "$(CURDIR)/scripts/livecodebench_eval.py" run --lcb-root "$(LCB_ROOT_ABS)" --runner-command-file "$(LCB_RUNNER_COMMAND_FILE_ABS)"

capability-smoke: capability-smoke-unit

# This mock-first contract is offline and credential-free. SKIP_TESTS=true is
# the only bypass; it is explicit, noisy, and cannot enable provider traffic.
capability-smoke-contracts:
	@if [ "$${SKIP_TESTS:-false}" = "true" ]; then \
		echo "WARNING: SKIP_TESTS=true skips capability-smoke-contracts (synthetic manifests, evidence verifier, redaction checks)"; \
	elif [ "$${SKIP_TESTS:-false}" = "false" ]; then \
		$(PYTHON) scripts/provider_capability_smoke.py unit; \
		$(PYTHON) scripts/provider_capability_smoke_test.py; \
	else \
		echo "SKIP_TESTS must be true or false" >&2; exit 2; \
	fi

capability-smoke-unit: capability-smoke-contracts
	@if [ "$${SKIP_TESTS:-false}" = "true" ]; then \
		echo "WARNING: SKIP_TESTS=true skips capability-smoke-unit Go tests"; \
	elif [ "$${SKIP_TESTS:-false}" = "false" ]; then \
		GOOS=$(HOST_GOOS) GOARCH=$(HOST_GOARCH) go test ./internal/router -run 'TestVerifyCapability'; \
	else \
		echo "SKIP_TESTS must be true or false" >&2; exit 2; \
	fi

# Reserved for a separately reviewed protected-live implementation. It always
# fails closed and the invoked script has no network, credential, or write path.
capability-smoke-live:
	@$(PYTHON) scripts/provider_capability_smoke.py live

.PHONY: test-k8s-nvidia-local-serving test-k8s-amd-instinct-local-serving test-k8s-nvidia-llmd-compat

# Offline gate for nvidia-local-serving blueprint + Kustomize overlay.
# Live Shadeform steps are documented in docs/SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md.
test-k8s-nvidia-local-serving:
	bash scripts/test_k8s_nvidia_local_serving.sh

# Offline gate for the manual k3s AMD Instinct vLLM/ROCm serving overlay.
# Live on-prem steps are documented in docs/K3S_AMD_INSTINCT_LOCAL_SERVING_E2E.md.
test-k8s-amd-instinct-local-serving:
	bash scripts/test_k8s_amd_instinct_local_serving.sh

# Offline gate for nvidia-llmd-compat blueprint (llm-d frontend + vLLM backend).
# Live steps: docs/SHADEFORM_NVIDIA_LLMD_COMPAT_E2E.md
test-k8s-nvidia-llmd-compat:
	bash scripts/test_k8s_nvidia_llmd_compat.sh

# Pull-request gate. Credential-free and free of disposable uv/Go environments.
# go test ./... covers focused Go packages, including capability and architecture.
test-fast: secret-contract capability-smoke-contracts
	go test ./...
	python3 scripts/outcome_calibrated_policy_test.py
	python3 scripts/api_compat_bootstrap_test.py
	"$${MAKE:-make}" harbor-adapter-test
	python3 scripts/ci_topology_test.py
	python3 scripts/harbor_promotion_test.py

# Full credential-free suite. Heavy targets stay behind shell Make invocations so
# `make -n` does not execute them. test-full is the merge-queue name for `test`.
test: test-fast secret-check
	"$${MAKE:-make}" api-compat-mock \
		$(call api_compat_make_data,API_COMPAT_BOOTSTRAP_GO_PROXY) \
		$(call api_compat_make_data,API_COMPAT_BOOTSTRAP_GO_SUMDB)
	"$${MAKE:-make}" harbor-local

test-full: test

# Offline proof that one dynamic_score group can select different upstreams for
# trivial vs complex fixtures, using the real request-evidence handler.
proof-routing:
	go test ./internal/router -run '^TestProofRoutingSameGroupDifferentUpstream$$' -count=1

# Offline AGENT-01..06 adapter contracts. Stdlib unittest only: no Harbor
# install, no provider network, and missing credentials stay blocked evidence.
harbor-adapter-test:
	$(PYTHON) -m unittest discover -s tests/harbor -p 'test_adapter*.py' -q

# Provision the locked Python and Go dependency sets before entering the
# isolated conformance run. API_COMPAT_BOOTSTRAP_GO_PROXY and
# API_COMPAT_BOOTSTRAP_GO_SUMDB permit an approved internal mirror; they apply
# only here, never while the suite is running offline.
API_COMPAT_BOOTSTRAP_GO_PROXY ?= https://proxy.golang.org
API_COMPAT_BOOTSTRAP_GO_SUMDB ?= sum.golang.org
# Command-line variables otherwise propagate through Make's recursive
# environment handling, which may expand their Make syntax before this recipe.
# Keep them Make-local and inject the literal values only into `go mod download`.
unexport API_COMPAT_BOOTSTRAP_GO_PROXY API_COMPAT_BOOTSTRAP_GO_SUMDB
# Quote raw Make values as one POSIX-shell word at the sole consumer. In
# particular, $(value ...) prevents a command-line override containing Make
# syntax from being expanded before the shell sees it.
api_compat_shell_data = '$(subst ','"'"',$(value $(1)))'
api_compat_make_data = $(1)=$(call api_compat_shell_data,$(1))
# GNU Make does not preserve -n through every recursive recipe it executes
# for inspection. Its first MAKEFLAGS word is the compact short-option word,
# so accept n inside that word (for example ns or sn), but never inspect later
# long-option words. Carry only that non-sensitive flag explicitly; mirror
# assignments remain excluded from the offline child below.
api_compat_dry_run_flag = $(if $(findstring n,$(filter-out --%,$(firstword $(MAKEFLAGS)))),-n)
api_compat_effective_dry_run_flag = $(or $(call api_compat_dry_run_flag),$(API_COMPAT_MAKE_DRY_RUN))
api_compat_dry_run_transport = $(if $(call api_compat_effective_dry_run_flag),API_COMPAT_MAKE_DRY_RUN=$(call api_compat_effective_dry_run_flag))
api-compat-bootstrap:
	@api_compat_root=$${API_COMPAT_BOOTSTRAP_ROOT:-$$(mktemp -d)}; \
	cd tests/api_compat && \
	env -u API_COMPAT_BOOTSTRAP_GO_PROXY -u API_COMPAT_BOOTSTRAP_GO_SUMDB \
		-u MAKEFLAGS -u MAKEOVERRIDES \
	UV_CACHE_DIR=$${UV_CACHE_DIR:-$$api_compat_root/uv-cache} \
	UV_PROJECT_ENVIRONMENT=$${UV_PROJECT_ENVIRONMENT:-$$api_compat_root/venv} \
	uv sync --locked && \
	env -u MAKEFLAGS -u MAKEOVERRIDES \
	API_COMPAT_BOOTSTRAP_ROOT="$$api_compat_root" \
	API_COMPAT_BOOTSTRAP_GO_PROXY=$(call api_compat_shell_data,API_COMPAT_BOOTSTRAP_GO_PROXY) \
	API_COMPAT_BOOTSTRAP_GO_SUMDB=$(call api_compat_shell_data,API_COMPAT_BOOTSTRAP_GO_SUMDB) \
	"$${MAKE:-make}" --no-print-directory -C ../.. api-compat-bootstrap-go-provision

# The approved mirror settings are consumed only by Go provisioning. Keep the
# #694 shell-data quoting at that sole consumer so explicit Make command-line
# values remain literal configuration data.
api-compat-bootstrap-go-provision:
	@GOMODCACHE=$${GOMODCACHE:-$${API_COMPAT_BOOTSTRAP_ROOT}/go-mod-cache} \
	GOCACHE=$${GOCACHE:-$${API_COMPAT_BOOTSTRAP_ROOT}/go-build-cache} \
	GOTOOLCHAIN=local \
	GOPROXY=$(call api_compat_shell_data,API_COMPAT_BOOTSTRAP_GO_PROXY) GOSUMDB=$(call api_compat_shell_data,API_COMPAT_BOOTSTRAP_GO_SUMDB) go mod download

# Deterministic caller-boundary tests: the locally built router and fake
# upstream bind to 127.0.0.1 only. This target is fail-closed: its Python and
# Go dependency resolution cannot use the network. It intentionally has no
# bootstrap prerequisite so clean-cache failure remains directly testable.
api-compat-mock-offline:
	cd tests/api_compat && PYTHONDONTWRITEBYTECODE=1 UV_OFFLINE=1 GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local uv run --locked --offline pytest -p no:cacheprovider

# Keep the default target usable on a clean supported runner while preserving
# the separately invokable fail-closed offline conformance phase. The shared
# disposable directory prevents ordinary invocations from creating .venv or
# dependency caches in the source tree.
api-compat-mock:
	@api_compat_root=$$(mktemp -d); \
	trap 'chmod -R u+w "$$api_compat_root" 2>/dev/null; rm -rf "$$api_compat_root"' EXIT; \
	UV_CACHE_DIR="$$api_compat_root/uv-cache" \
	UV_PROJECT_ENVIRONMENT="$$api_compat_root/venv" \
	GOMODCACHE="$$api_compat_root/go-mod-cache" \
	GOCACHE="$$api_compat_root/go-build-cache" \
	GOTOOLCHAIN=local \
	$(MAKE) $(call api_compat_effective_dry_run_flag) $(call api_compat_dry_run_transport) api-compat-bootstrap API_COMPAT_BOOTSTRAP_ROOT="$$api_compat_root" \
		$(call api_compat_make_data,API_COMPAT_BOOTSTRAP_GO_PROXY) \
		$(call api_compat_make_data,API_COMPAT_BOOTSTRAP_GO_SUMDB) && \
	UV_CACHE_DIR="$$api_compat_root/uv-cache" \
	UV_PROJECT_ENVIRONMENT="$$api_compat_root/venv" \
	GOMODCACHE="$$api_compat_root/go-mod-cache" \
	GOCACHE="$$api_compat_root/go-build-cache" \
	GOTOOLCHAIN=local \
	env -u API_COMPAT_BOOTSTRAP_GO_PROXY -u API_COMPAT_BOOTSTRAP_GO_SUMDB \
		-u MAKEFLAGS -u MAKEOVERRIDES \
		$(MAKE) $(call api_compat_effective_dry_run_flag) --no-print-directory api-compat-mock-offline

# Harbor P0 local-task dataset (HARBOR-01..06): offline verifier integrity and
# protocol simulations. Does not run real agents or contact providers.
harbor-local-offline:
	cd tests/harbor && PYTHONDONTWRITEBYTECODE=1 UV_OFFLINE=1 uv run --locked --offline pytest -p no:cacheprovider

harbor-local:
	@harbor_root=$$(mktemp -d); \
	trap 'chmod -R u+w "$$harbor_root" 2>/dev/null; rm -rf "$$harbor_root"' EXIT; \
	cd tests/harbor && \
	UV_CACHE_DIR="$$harbor_root/uv-cache" \
	UV_PROJECT_ENVIRONMENT="$$harbor_root/venv" \
	uv sync --locked && \
	UV_CACHE_DIR="$$harbor_root/uv-cache" \
	UV_PROJECT_ENVIRONMENT="$$harbor_root/venv" \
	PYTHONDONTWRITEBYTECODE=1 UV_OFFLINE=1 \
	uv run --locked --offline pytest -p no:cacheprovider

# Deliberately not a normal test/build/package target. Live execution requires a
# human-approved non-production matrix, least-privilege caller, and budget caps.
# Missing credentials report blocked (nonzero) and never green certification.
API_COMPAT_LIVE_MATRIX ?=
API_COMPAT_LIVE_ENVIRONMENT ?=
API_COMPAT_LIVE_CALLER ?=
API_COMPAT_LIVE_BASE_URL ?=
API_COMPAT_LIVE_CREDENTIAL_FILE ?=
API_COMPAT_LIVE_CONFIRM ?=
export API_COMPAT_LIVE_MATRIX API_COMPAT_LIVE_ENVIRONMENT API_COMPAT_LIVE_CALLER API_COMPAT_LIVE_BASE_URL API_COMPAT_LIVE_CREDENTIAL_FILE API_COMPAT_LIVE_CONFIRM
api-compat-live:
	@test -n "$${API_COMPAT_LIVE_MATRIX}" && test "$${API_COMPAT_LIVE_MATRIX}" != "default" && test "$${API_COMPAT_LIVE_MATRIX}" != "all" || { echo "API_COMPAT_LIVE_MATRIX must name an approved matrix" >&2; exit 2; }
	@test -n "$${API_COMPAT_LIVE_ENVIRONMENT}" && test "$${API_COMPAT_LIVE_ENVIRONMENT}" != "production" && test "$${API_COMPAT_LIVE_ENVIRONMENT}" != "default" || { echo "API_COMPAT_LIVE_ENVIRONMENT must name a non-production environment" >&2; exit 2; }
	@test -n "$${API_COMPAT_LIVE_CALLER}" && test "$${API_COMPAT_LIVE_CALLER}" != "default" && test "$${API_COMPAT_LIVE_CALLER}" != "all" || { echo "API_COMPAT_LIVE_CALLER must name a least-privilege caller" >&2; exit 2; }
	@test -n "$${API_COMPAT_LIVE_BASE_URL}" || { echo "API_COMPAT_LIVE_BASE_URL is required" >&2; exit 2; }
	@test -f "$${API_COMPAT_LIVE_CREDENTIAL_FILE}" && test "$$(stat -c '%a' "$${API_COMPAT_LIVE_CREDENTIAL_FILE}")" = 600 || { echo "API_COMPAT_LIVE_CREDENTIAL_FILE must be a mode-0600 protected file" >&2; exit 2; }
	@test "$${API_COMPAT_LIVE_CONFIRM}" = "$${API_COMPAT_LIVE_MATRIX}:$${API_COMPAT_LIVE_ENVIRONMENT}" || { echo "API_COMPAT_LIVE_CONFIRM must bind the selected matrix and environment" >&2; exit 2; }
	@$(PYTHON) scripts/api_compat_live.py

outcome-calibrated-demo: outcome-calibrated-synthetic-demo

outcome-calibrated-synthetic-demo:
	python3 scripts/run_outcome_calibrated_demo.py --out-dir tmp/outcome-calibrated-demo

adaptive-signal-policy-demo:
	python3 scripts/run_adaptive_signal_policy_demo.py

openjev-routing-demo:
	python3 scripts/openjev_policy_test.py
	python3 scripts/run_openjev_routing_demo.py

.PHONY: dco-check-test
dco-check-test:
	python3 scripts/check_dco_test.py

# Generate deterministic, credential-free synthetic Responses fixtures.
sse-capture:
	python3 scripts/sse_capture.py

secret-contract:
	python3 scripts/check_env_example_secrets.py
	python3 scripts/check_env_example_secrets_test.py
	python3 scripts/canonical_product_test.py
	python3 scripts/check_stale_product_names_test.py
	python3 scripts/check_stale_product_names.py --enforce-docs-origin --enforce-contract

secret-check: secret-contract
	python3 scripts/local_dev_bootstrap_test.py
	python3 scripts/validate_production_bundle_config_test.py

	python3 scripts/launch_operational_readiness_test.py
	python3 scripts/validate_package_contents_test.py
	python3 scripts/release_artifact_inventory_test.py
	python3 scripts/validate_release_clean_test.py
	python3 scripts/compose_package_upgrade_test.py
	python3 scripts/compose_clean_cutover_test.py
	python3 scripts/backup_dist_restic.py --self-test

	python3 scripts/validate_docker_context.py
	python3 scripts/harness_security_test.py
	python3 scripts/makefile_security_test.py
	python3 scripts/api_compat_live_test.py
	python3 scripts/bootstrap_eks_session_test.py
	python3 scripts/eks_discover_test.py
	python3 scripts/validate_eks_make_args_test.py
	python3 scripts/render_tenant_ingress_network_policy_test.py
	python3 scripts/render_tenant_linkerd_policy_test.py
	python3 scripts/apply_tenant_network_policies_test.py
	python3 scripts/check_docs_public_face_test.py
	$(MAKE) validate-build-metadata


validate-build-metadata:
	python3 scripts/validate_build_metadata.py

validate-release-clean:
	python3 scripts/validate_release_clean.py

release-notes-from-git:
	python3 scripts/release_notes_from_git.py

release-validation-matrix:
	python3 scripts/validate_release_matrix.py

launch-operational-readiness:
	python3 scripts/launch_operational_readiness.py

release-artifact-inventory:
	python3 scripts/release_artifact_inventory.py --dist-dir "$${DIST_DIR}" --version "$${VERSION}" --commit "$${COMMIT}" --build-date "$${BUILD_DATE}"

release-security-evidence:
	python3 scripts/release_security_evidence.py --dist-dir "$${DIST_DIR}" --version "$${VERSION}" $${RELEASE_SECURITY_ARGS}

docs-diag-schema:
	go run ./cmd/docs-diag-schema --write

docs-diag-schema-check:
	go run ./cmd/docs-diag-schema --check

.PHONY: docs-brand-policy-test
docs-brand-policy-test:
	python3 scripts/check_docs_public_face_test.py

docs-qa: docs-diag-schema-check docs-brand-policy-test
	python3 scripts/check_docs_public_face.py
	python3 scripts/validate_docs_versioning.py
	python3 scripts/check_docs_sidebar.py
	go run ./cmd/docs-config-example-check

docs-build: docs-qa
	cd "$(DOCS_SITE_DIR)" && npm ci && DOCS_ROUTER_VERSION="$${VERSION}" DOCS_ROUTER_BUILD_DATE="$${BUILD_DATE}" npm run build
	find "$(DOCS_EMBED_DIR)" -mindepth 1 ! -name .keep -exec rm -rf {} +
	cp -R "$(DOCS_SITE_DIR)/build/." "$(DOCS_EMBED_DIR)/"

docs-dev:
	cd "$(DOCS_SITE_DIR)" && npm install && npm run start

docs-clean:
	rm -rf "$(DOCS_SITE_DIR)/build" "$(DOCS_SITE_DIR)/.docusaurus"
	find "$(DOCS_EMBED_DIR)" -mindepth 1 ! -name .keep -exec rm -rf {} +

admin-build:
	rm -rf internal/router/admindist/static/assets
	npm ci --prefix internal/router/admindist/web
	npm run build --prefix internal/router/admindist/web

admin-e2e: admin-build
	npx --prefix internal/router/admindist/web playwright install chromium
	npm run e2e --prefix internal/router/admindist/web

build-package-binaries:
	@set -e; \
	for bin in $(PACKAGE_BINARIES); do \
		echo "building $$bin"; \
		go build -buildvcs=false -ldflags "$(BUILD_LDFLAGS)" -o "$$bin" "./cmd/$$bin"; \
	done

build: docs-build admin-build capability-smoke-unit
	$(MAKE) validate-build-metadata
	$(MAKE) build-package-binaries

build-go-only: capability-smoke-unit
	$(MAKE) validate-build-metadata
	$(MAKE) build-package-binaries

build-all: docs-build admin-build capability-smoke-unit
	$(MAKE) validate-build-metadata
	mkdir -p "$${DIST_DIR}/build/linux-amd64" "$${DIST_DIR}/build/linux-arm64"
	@set -e; \
	for arch in amd64 arm64; do \
		for bin in $(PACKAGE_BINARIES); do \
			echo "building linux-$$arch/$$bin"; \
			CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -buildvcs=false -ldflags "$(BUILD_LDFLAGS)" -o "$${DIST_DIR}/build/linux-$$arch/$$bin" "./cmd/$$bin"; \
		done; \
	done

package: package-all

package-one: validate-release-clean docs-build admin-build capability-smoke-unit
	RELEASE_CLEAN_VALIDATED=1 $(MAKE) package-one-no-docs

package-one-no-docs: capability-smoke-unit
	@if [ "$${RELEASE_CLEAN_VALIDATED}" != "1" ]; then $(MAKE) validate-release-clean; fi
	$(MAKE) validate-build-metadata
	pkg_dir="$${DIST_DIR}/pkg/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}"; \
	rm -rf "$${pkg_dir}"; \
	mkdir -p "$${pkg_dir}/bin" "$${pkg_dir}/config/scripts" "$${pkg_dir}/docs" "$${pkg_dir}/caddy"; \
	set -e; \
	for bin in $(PACKAGE_BINARIES); do \
		echo "packaging $$bin for $${GOOS}/$${GOARCH}"; \
		CGO_ENABLED=0 GOOS="$${GOOS}" GOARCH="$${GOARCH}" go build -buildvcs=false -ldflags "$(BUILD_LDFLAGS)" -o "$${pkg_dir}/bin/$$bin" "./cmd/$$bin"; \
	done; \
	cp config.example.yaml "$${pkg_dir}/config/config.example.yaml"; \
	cp env.example.json "$${pkg_dir}/config/env.example.json"; \
	cp scripts/router.ts "$${pkg_dir}/config/scripts/router.ts"; \
	cp deploy/Caddyfile "$${pkg_dir}/caddy/Caddyfile"; \
	for legal in LICENSE NOTICE THIRD_PARTY_NOTICES.md MODEL_LICENSES.md; do \
		cp "$$legal" "$${pkg_dir}/$$legal"; \
	done; \
	while IFS= read -r doc; do \
		case "$$doc" in ""|\#*) continue ;; esac; \
		cp "$$doc" "$${DIST_DIR}/pkg/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}/docs/$$(basename "$$doc")"; \
	done < "$(PACKAGE_DOC_ALLOWLIST)"
	find "$${DIST_DIR}/pkg/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}" -type d -exec chmod 0755 {} \;
	find "$${DIST_DIR}/pkg/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}" -type f -exec chmod 0644 {} \;
	chmod 0755 $${DIST_DIR}/pkg/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}/bin/*
	$(TAR_ENV) tar --owner=0 --group=0 --numeric-owner -C "$${DIST_DIR}/pkg" -czf "$${DIST_DIR}/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}.tar.gz" "$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}"
	python3 scripts/validate_package_contents.py --allowlist "$(PACKAGE_DOC_ALLOWLIST)" "$${DIST_DIR}/$${PKG_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}.tar.gz"
package-all: validate-release-clean docs-build admin-build capability-smoke-unit
	RELEASE_CLEAN_VALIDATED=1 GOOS=linux GOARCH=amd64 $(MAKE) package-one-no-docs
	RELEASE_CLEAN_VALIDATED=1 GOOS=linux GOARCH=arm64 $(MAKE) package-one-no-docs

docker-image: docs-build admin-build capability-smoke-unit docker-image-no-docs

docker-image-no-docs: capability-smoke-unit
	$(MAKE) validate-build-metadata
	$(DOCKER_BUILDX) build --platform "$(DOCKER_PLATFORM)" --load --build-arg "VERSION=$${VERSION}" --build-arg "COMMIT=$${COMMIT}" --build-arg "BUILD_DATE=$${BUILD_DATE}" -t "$${IMAGE_NAME}:$${IMAGE_TAG}" .

package-docker: package-docker-all

package-docker-one: validate-release-clean docs-build admin-build capability-smoke-unit
	RELEASE_CLEAN_VALIDATED=1 $(MAKE) package-docker-one-no-docs

package-docker-one-no-docs: capability-smoke-unit
	@if [ "$${RELEASE_CLEAN_VALIDATED}" != "1" ]; then $(MAKE) validate-release-clean; fi
	$(MAKE) validate-build-metadata
	docker_pkg_dir="$${DIST_DIR}/docker/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}"; \
	rm -rf "$${docker_pkg_dir}"; \
	mkdir -p "$${docker_pkg_dir}/images" "$${docker_pkg_dir}/compose" "$${docker_pkg_dir}/config/scripts" "$${docker_pkg_dir}/docs"
	GOOS="$${GOOS}" GOARCH="$${GOARCH}" DOCKER_PLATFORM="linux/$${GOARCH}" IMAGE_TAG="$${VERSION}-$${GOOS}-$${GOARCH}" $(MAKE) docker-image-no-docs
	docker_pkg_dir="$${DIST_DIR}/docker/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}"; \
	$(DOCKER) save "$${IMAGE_NAME}:$${VERSION}-$${GOOS}-$${GOARCH}" -o "$${docker_pkg_dir}/images/$${IMAGE_NAME}-$${VERSION}-$${GOOS}-$${GOARCH}.tar"; \
	cp deploy/docker-compose.yml "$${docker_pkg_dir}/compose/docker-compose.yml"; \
	cp deploy/docker-compose.postgres-localhost.yml "$${docker_pkg_dir}/compose/docker-compose.postgres-localhost.yml"; \
	cp deploy/Caddyfile.compose "$${docker_pkg_dir}/compose/Caddyfile.compose"; \
	cp deploy/compose.env.example "$${docker_pkg_dir}/compose/.env.example"; \
	sed "s/^METRUM_AI_ROUTER_VERSION=.*/METRUM_AI_ROUTER_VERSION=$${VERSION}-$${GOOS}-$${GOARCH}/" deploy/compose.env.example > "$${docker_pkg_dir}/compose/.env"; \
	cp config.example.yaml "$${docker_pkg_dir}/config/config.example.yaml"; \
	cp env.example.json "$${docker_pkg_dir}/config/env.example.json"; \
	cp scripts/router.ts "$${docker_pkg_dir}/config/scripts/router.ts"; \
	for legal in LICENSE NOTICE THIRD_PARTY_NOTICES.md MODEL_LICENSES.md; do \
		cp "$$legal" "$${docker_pkg_dir}/$$legal"; \
	done; \
	while IFS= read -r doc; do \
		case "$$doc" in ""|\#*) continue ;; esac; \
		cp "$$doc" "$${DIST_DIR}/docker/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}/docs/$$(basename "$$doc")"; \
	done < "$(PACKAGE_DOC_ALLOWLIST)"
	find "$${DIST_DIR}/docker/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}" -type d -exec chmod 0755 {} \;
	find "$${DIST_DIR}/docker/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}" -type f -exec chmod 0644 {} \;
	$(TAR_ENV) tar --owner=0 --group=0 --numeric-owner -C "$${DIST_DIR}/docker" -czf "$${DIST_DIR}/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}.tar.gz" "$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}"
	python3 scripts/validate_package_contents.py --allowlist "$(PACKAGE_DOC_ALLOWLIST)" "$${DIST_DIR}/$${PKG_NAME}-$${VERSION}-docker-$${GOOS}-$${GOARCH}.tar.gz"

package-docker-all: validate-release-clean docs-build admin-build capability-smoke-unit
	RELEASE_CLEAN_VALIDATED=1 GOOS=linux GOARCH=amd64 $(MAKE) package-docker-one-no-docs
	RELEASE_CLEAN_VALIDATED=1 GOOS=linux GOARCH=arm64 $(MAKE) package-docker-one-no-docs

# Back up one complete release set from DIST_DIR to the Metrum CTO restic
# repository using stable snapshot basenames (version stays in local filenames
# and restic tags). Requires BACKUP_USER, BACKUP_PASS, and RESTIC_PASSWORD from
# ignored env.json (or the process environment). Never prints those values.
# Includes binary packages and the shared customer Docker packages;
# customer license files stay out of packages and out of this backup.
dist-backup:
	$(PYTHON) scripts/backup_dist_restic.py --dist-dir "$${DIST_DIR}" --env-json env.json --version "$${VERSION}"

# Build both package families, then restic-backup the release tarballs.
package-dist-backup: package-all package-docker-all dist-backup

compose-security-check:
	bash scripts/check_compose_security.sh

eks-session-bootstrap:
	@test -n "$$EKS_CLEANUP_RECORD" || (echo "EKS_CLEANUP_RECORD is required" >&2; exit 2)
	python3 scripts/bootstrap_eks_session.py --admin-profile "$$EKS_ADMIN_PROFILE" --source-user "$$EKS_SOURCE_USER" --mfa-serial "$$EKS_MFA_SERIAL" --macos-keychain-service "$$EKS_MFA_KEYCHAIN_SERVICE" --macos-keychain-account "$$EKS_MFA_KEYCHAIN_ACCOUNT" --session-profile smartrouter --role-profile "$$EKS_AWS_PROFILE" --role-arn "arn:aws:iam::$$EKS_ACCOUNT_ID:role/metrum-ai-router-eks-discovery" --region "$$EKS_REGION" --duration-seconds "$$EKS_SESSION_DURATION" --cleanup-record "$$EKS_CLEANUP_RECORD"

eks-session-recovery-status:
	@test -n "$$EKS_CLEANUP_RECORD" || (echo "EKS_CLEANUP_RECORD is required" >&2; exit 2)
	python3 scripts/bootstrap_eks_session.py --recovery-status "$$EKS_CLEANUP_RECORD"

eks-identity-check:
	python3 scripts/validate_eks_make_args.py --identity-only --profile "$$EKS_AWS_PROFILE" --account-id "$$EKS_ACCOUNT_ID" --region "$$EKS_REGION"
	@identity="$$(aws --profile "$$EKS_AWS_PROFILE" --region "$$EKS_REGION" sts get-caller-identity --query Arn --output text)"; \
	case "$$identity" in \
	"arn:aws:sts::$$EKS_ACCOUNT_ID:assumed-role/metrum-ai-router-eks-discovery/"*) ;; \
	*) echo "EKS identity error: expected the metrum-ai-router-eks-discovery assumed role" >&2; exit 1 ;; \
	esac

eks-discovery-validate:
	python3 scripts/validate_eks_make_args.py --profile "$$EKS_AWS_PROFILE" --account-id "$$EKS_ACCOUNT_ID" --region "$$EKS_REGION" --cluster "$$EKS_CLUSTER" --namespace "$$EKS_NAMESPACE" --linkerd-namespace "$$EKS_LINKERD_NAMESPACE" --ingress-namespace "$$EKS_INGRESS_NAMESPACE" --ingress-service-account "$$EKS_INGRESS_SERVICE_ACCOUNT" --ingress-deployment "$$EKS_INGRESS_DEPLOYMENT" --linkerd-trust-domain "$$EKS_LINKERD_TRUST_DOMAIN" --ecr-repository "$$EKS_ECR_REPOSITORY" --output "$$EKS_DISCOVERY_OUTPUT"

eks-discover: eks-discovery-validate
	if [ -n "$$EKS_LINKERD_NAMESPACE" ]; then \
		python3 scripts/eks_discover.py --profile "$$EKS_AWS_PROFILE" --account-id "$$EKS_ACCOUNT_ID" --region "$$EKS_REGION" --cluster "$$EKS_CLUSTER" --namespace "$$EKS_NAMESPACE" --linkerd-namespace "$$EKS_LINKERD_NAMESPACE" --ingress-namespace "$$EKS_INGRESS_NAMESPACE" --ingress-service-account "$$EKS_INGRESS_SERVICE_ACCOUNT" --ingress-deployment "$$EKS_INGRESS_DEPLOYMENT" --linkerd-trust-domain "$$EKS_LINKERD_TRUST_DOMAIN" --ecr-repository "$$EKS_ECR_REPOSITORY" --output "$$EKS_DISCOVERY_OUTPUT"; \
	else \
		python3 scripts/eks_discover.py --profile "$$EKS_AWS_PROFILE" --account-id "$$EKS_ACCOUNT_ID" --region "$$EKS_REGION" --cluster "$$EKS_CLUSTER" --namespace "$$EKS_NAMESPACE" --ingress-namespace "$$EKS_INGRESS_NAMESPACE" --ecr-repository "$$EKS_ECR_REPOSITORY" --output "$$EKS_DISCOVERY_OUTPUT"; \
	fi

eks-render-ingress-network-policy:
	@test -n "$$EKS_DISCOVERY_OUTPUT" || (echo "EKS_DISCOVERY_OUTPUT is required" >&2; exit 2)
	@test -n "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT" || (echo "EKS_INGRESS_NETWORK_POLICY_OUTPUT is required" >&2; exit 2)
	python3 scripts/render_tenant_ingress_network_policy.py --discovery-report "$$EKS_DISCOVERY_OUTPUT" --output "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT"

eks-render-linkerd-policy: eks-render-ingress-network-policy
	@test -n "$$EKS_LINKERD_POLICY_OUTPUT" || (echo "EKS_LINKERD_POLICY_OUTPUT is required" >&2; exit 2)
	python3 scripts/render_tenant_linkerd_policy.py --discovery-report "$$EKS_DISCOVERY_OUTPUT" --output "$$EKS_LINKERD_POLICY_OUTPUT"

eks-validate-tenant-network-policies:
	@test -n "$$EKS_DISCOVERY_OUTPUT" || (echo "EKS_DISCOVERY_OUTPUT is required" >&2; exit 2)
	@test -n "$$EKS_POLICY_AWS_PROFILE" || (echo "EKS_POLICY_AWS_PROFILE is required" >&2; exit 2)
	@test -n "$$EKS_POLICY_KUBECONFIG" || (echo "EKS_POLICY_KUBECONFIG is required" >&2; exit 2)
	@test -n "$$EKS_POLICY_CONTEXT" || (echo "EKS_POLICY_CONTEXT is required" >&2; exit 2)
	@test -n "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT" || (echo "EKS_INGRESS_NETWORK_POLICY_OUTPUT is required" >&2; exit 2)
	@if [ -n "$$EKS_LINKERD_POLICY_OUTPUT" ]; then \
		python3 scripts/apply_tenant_network_policies.py --discovery-report "$$EKS_DISCOVERY_OUTPUT" --profile "$$EKS_POLICY_AWS_PROFILE" --kubeconfig "$$EKS_POLICY_KUBECONFIG" --context "$$EKS_POLICY_CONTEXT" --ingress-policy "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT" --linkerd-policy "$$EKS_LINKERD_POLICY_OUTPUT"; \
	else \
		python3 scripts/apply_tenant_network_policies.py --discovery-report "$$EKS_DISCOVERY_OUTPUT" --profile "$$EKS_POLICY_AWS_PROFILE" --kubeconfig "$$EKS_POLICY_KUBECONFIG" --context "$$EKS_POLICY_CONTEXT" --ingress-policy "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT"; \
	fi

eks-apply-tenant-network-policies:
	@test "$$EKS_POLICY_APPLY_CONFIRM" = "apply" || (echo "EKS_POLICY_APPLY_CONFIRM=apply is required" >&2; exit 2)
	@test -n "$$EKS_DISCOVERY_OUTPUT" || (echo "EKS_DISCOVERY_OUTPUT is required" >&2; exit 2)
	@test -n "$$EKS_POLICY_AWS_PROFILE" || (echo "EKS_POLICY_AWS_PROFILE is required" >&2; exit 2)
	@test -n "$$EKS_POLICY_KUBECONFIG" || (echo "EKS_POLICY_KUBECONFIG is required" >&2; exit 2)
	@test -n "$$EKS_POLICY_CONTEXT" || (echo "EKS_POLICY_CONTEXT is required" >&2; exit 2)
	@test -n "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT" || (echo "EKS_INGRESS_NETWORK_POLICY_OUTPUT is required" >&2; exit 2)
	@if [ -n "$$EKS_LINKERD_POLICY_OUTPUT" ]; then \
		python3 scripts/apply_tenant_network_policies.py --discovery-report "$$EKS_DISCOVERY_OUTPUT" --profile "$$EKS_POLICY_AWS_PROFILE" --kubeconfig "$$EKS_POLICY_KUBECONFIG" --context "$$EKS_POLICY_CONTEXT" --ingress-policy "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT" --linkerd-policy "$$EKS_LINKERD_POLICY_OUTPUT" --apply; \
	else \
		python3 scripts/apply_tenant_network_policies.py --discovery-report "$$EKS_DISCOVERY_OUTPUT" --profile "$$EKS_POLICY_AWS_PROFILE" --kubeconfig "$$EKS_POLICY_KUBECONFIG" --context "$$EKS_POLICY_CONTEXT" --ingress-policy "$$EKS_INGRESS_NETWORK_POLICY_OUTPUT" --apply; \
	fi

e2e-mock:
	$(MAKE) -C examples/cli-e2e-c clean test

e2e-live-c: build
	bash scripts/live_cli_c_e2e.sh

e2e-live-full: build
	bash scripts/live_full_e2e.sh

e2e-compose-live:
	bash scripts/compose_live_e2e.sh

.PHONY: lrp-test lrp-synthetic-demo lrp-e2e lrp-routing-benchmark-harness-test
LRP_PROJECT := services/learned-routing-policy
LRP_DEMO_DIR ?= /var/tmp/metrum-lrp-synthetic-demo

lrp-test:
	uv run --project $(LRP_PROJECT) --locked ruff check --config $(LRP_PROJECT)/pyproject.toml $(LRP_PROJECT)/lrp $(LRP_PROJECT)/tests
	uv run --project $(LRP_PROJECT) --locked mypy --config-file $(LRP_PROJECT)/pyproject.toml --strict $(LRP_PROJECT)/lrp
	env TMPDIR=/var/tmp uv run --project $(LRP_PROJECT) --locked pytest $(LRP_PROJECT)/tests -q $(if $(LRP_JUNIT_REPORT),--junitxml=$(LRP_JUNIT_REPORT),)

# Offline #159 harness/schema tests only. Does not run live or paid benchmarks.
lrp-routing-benchmark-harness-test:
	python3 scripts/lrp_routing_benchmark_test.py -q
	python3 scripts/lrp_routing_benchmark.py validate \
		--document docs/evidence/learned-routing-policy/routing-benchmark.json \
		--schema docs/evidence/learned-routing-policy/routing-benchmark.schema.json

lrp-synthetic-demo:
	uv run --project $(LRP_PROJECT) --locked python scripts/run_lrp_synthetic_demo.py --out-dir $(LRP_DEMO_DIR)

lrp-e2e: lrp-synthetic-demo
	uv run --project $(LRP_PROJECT) --locked python scripts/run_lrp_synthetic_demo.py --out-dir $(LRP_DEMO_DIR) --e2e

clean:
	rm -rf router router-token router-token-gen router-usage-report examples/cli-e2e-c/cli-e2e "$${DIST_DIR}"
	rm -rf "$(DOCS_SITE_DIR)/build" "$(DOCS_SITE_DIR)/.docusaurus"
	find "$(DOCS_EMBED_DIR)" -mindepth 1 ! -name .keep -exec rm -rf {} +
