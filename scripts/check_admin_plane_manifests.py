#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Check the admin control-plane Kubernetes overlay for exposure mistakes (#817).

The overlay is opt-in, so these invariants are checked on its source files (CI
renders the same directory with kustomize and scans it with trivy):

- the admin listener is enabled and binds a port that the Deployment exposes;
- the admin Service is ClusterIP only and targets that port;
- no Ingress selects the admin Service;
- ingress is default-deny for the router pods;
- the admin port is allowed only from selected pods, never from every source;
- a non-loopback plaintext bind is explicitly acknowledged.
"""

from __future__ import annotations

import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
OVERLAY = ROOT / "deploy" / "kubernetes" / "overlays" / "admin-plane"


class Problems:
    def __init__(self) -> None:
        self.items: list[str] = []

    def check(self, condition: bool, message: str) -> None:
        if not condition:
            self.items.append(message)


def load_yaml(path: Path):
    with path.open("r", encoding="utf-8") as handle:
        return [doc for doc in yaml.safe_load_all(handle) if doc]


def load_documents(directory: Path) -> list[dict]:
    documents: list[dict] = []
    for path in sorted(directory.glob("*.y*ml")):
        documents.extend(load_yaml(path))
    return documents


def admin_config() -> dict:
    raw = yaml.safe_load((OVERLAY / "config.yaml").read_text(encoding="utf-8"))
    return raw.get("admin") or {}


def admin_listen_port(admin: dict) -> int | None:
    listen = str(admin.get("listen") or "")
    if ":" not in listen:
        return None
    _, _, port = listen.rpartition(":")
    try:
        return int(port)
    except ValueError:
        return None


def router_selector_matches(selector: dict | None) -> bool:
    if not selector:
        return False
    match_labels = selector.get("matchLabels") or {}
    return match_labels.get("app.kubernetes.io/name") == "metrum-ai-router"


def check(problems: Problems) -> None:
    admin = admin_config()
    problems.check(admin.get("enabled") is True, "admin.enabled must be true in the overlay config")
    port = admin_listen_port(admin)
    problems.check(port is not None, "admin.listen must be host:port")

    tls_mode = str((admin.get("tls") or {}).get("mode") or "off").lower()
    loopback = str(admin.get("listen") or "").startswith(("127.", "[::1]", "localhost"))
    if not loopback and tls_mode == "off":
        problems.check(
            admin.get("allow_plaintext_non_loopback") is True,
            "a non-loopback plaintext admin listener needs allow_plaintext_non_loopback: true",
        )

    documents = load_documents(OVERLAY)

    deployment_ports = set()
    for document in documents:
        if document.get("kind") != "Deployment":
            continue
        for container in ((document.get("spec") or {}).get("template") or {}).get("spec", {}).get("containers", []):
            for entry in container.get("ports") or []:
                if entry.get("name") == "admin":
                    deployment_ports.add(entry.get("containerPort"))
    problems.check(port in deployment_ports, f"the Deployment must expose containerPort {port} named admin")

    services = [doc for doc in documents if doc.get("kind") == "Service" and "admin" in str(doc.get("metadata", {}).get("name", ""))]
    problems.check(len(services) == 1, "exactly one admin Service is expected")
    for service in services:
        spec = service.get("spec") or {}
        problems.check(spec.get("type", "ClusterIP") == "ClusterIP", "the admin Service must be ClusterIP")
        service_ports = spec.get("ports") or []
        problems.check(
            any(entry.get("targetPort") == "admin" or entry.get("targetPort") == port for entry in service_ports),
            "the admin Service must target the admin container port",
        )

    ingresses = [doc for doc in documents if doc.get("kind") == "Ingress"]
    problems.check(not ingresses, "the admin overlay must not ship an Ingress")
    for ingress in ingresses:
        for rule in (ingress.get("spec") or {}).get("rules") or []:
            for path in ((rule.get("http") or {}).get("paths") or []):
                service_name = ((path.get("backend") or {}).get("service") or {}).get("name", "")
                problems.check("admin" not in service_name, "no Ingress path may target the admin Service")

    network_policies = [doc for doc in documents if doc.get("kind") == "NetworkPolicy"]
    problems.check(bool(network_policies), "the overlay must ship NetworkPolicies")
    deny_all = any(
        router_selector_matches(policy.get("spec", {}).get("podSelector"))
        and "Ingress" in (policy.get("spec", {}).get("policyTypes") or [])
        and policy.get("spec", {}).get("ingress") == []
        for policy in network_policies
    )
    problems.check(deny_all, "a default-deny ingress policy must select the router pods")

    admin_port_allowed_without_source = False
    admin_port_allowed_from_selected = False
    for policy in network_policies:
        spec = policy.get("spec") or {}
        if not router_selector_matches(spec.get("podSelector")):
            continue
        for rule in spec.get("ingress") or []:
            rule_ports = [entry.get("port") for entry in rule.get("ports") or []]
            if port not in rule_ports:
                continue
            if rule.get("from"):
                if any(
                    (source.get("podSelector") or {}).get("matchLabels", {}).get("access.metrum.ai/admin-client") == "true"
                    for source in rule["from"]
                ):
                    admin_port_allowed_from_selected = True
            else:
                admin_port_allowed_without_source = True
    problems.check(not admin_port_allowed_without_source, "no NetworkPolicy may allow the admin port from every source")
    problems.check(admin_port_allowed_from_selected, "the admin port must be allowed from selected admin-client pods")


def main() -> int:
    problems = Problems()
    check(problems)
    if problems.items:
        for item in problems.items:
            print(f"admin-plane overlay: {item}", file=sys.stderr)
        return 1
    print("admin-plane overlay ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
