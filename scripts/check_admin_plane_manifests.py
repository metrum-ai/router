#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Check the rendered admin control-plane overlay for exposure mistakes (#817).

The overlay is rendered with `kustomize build` first, so the base resources it
composes are checked too (a base Service or Ingress cannot hide behind the
overlay). Invariants:

- the admin listener is enabled, binds a port, and the Deployment exposes it;
- the admin Service is ClusterIP only and targets that port;
- no Ingress in the rendered output selects the admin Service;
- ingress is default-deny for the router pods;
- the admin port is reachable only from selected pods: a rule without `ports`
  allows every port, a rule without `from` allows every source, a named port is
  resolved through the Deployment, and a broad `ipBlock` is refused even when a
  narrow selector is also present;
- a non-loopback plaintext bind is explicitly acknowledged in the config.
"""

from __future__ import annotations

import ipaddress
import shutil
import subprocess
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
OVERLAY = ROOT / "deploy" / "kubernetes" / "overlays" / "admin-plane"
ADMIN_CLIENT_LABEL = "access.metrum.ai/admin-client"
ROUTER_APP_LABEL = "app.kubernetes.io/name"

# A deployment that must reach the admin port from a network range instead of a
# labelled pod (for example a bastion subnet) records it here as cidr -> reason.
# Every entry is printed by the check so the justification stays visible in CI;
# a range wider than /16 (IPv4) or /48 (IPv6) needs this entry *and* is reported
# on every run. The default is empty: the reference overlay uses labelled pods.
BROAD_IPBLOCK_OVERRIDES: dict[str, str] = {}


class Problems:
    def __init__(self) -> None:
        self.items: list[str] = []

    def check(self, condition: bool, message: str) -> None:
        if not condition:
            self.items.append(message)


def render_overlay(directory: Path) -> list[dict]:
    """Render the overlay with kustomize and return the resulting documents."""
    binary = shutil.which("kustomize")
    if binary is None:
        raise SystemExit(
            "kustomize is required to check the admin overlay (the check inspects rendered output, "
            "not source fragments). Install sigs.k8s.io/kustomize/kustomize/v5@v5.6.0."
        )
    result = subprocess.run([binary, "build", str(directory)], capture_output=True, text=True)
    if result.returncode != 0:
        raise SystemExit(f"kustomize build {directory} failed: {result.stderr.strip()[:400]}")
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


def router_selector_matches(selector: dict | None) -> bool:
    if not selector:
        return False
    return (selector.get("matchLabels") or {}).get(ROUTER_APP_LABEL) == "metrum-ai-router"


def admin_config(documents: list[dict]) -> dict:
    for document in documents:
        if document.get("kind") != "ConfigMap":
            continue
        embedded = (document.get("data") or {}).get("config.yaml")
        if not embedded:
            continue
        parsed = yaml.safe_load(embedded) or {}
        if "admin" in parsed:
            return parsed["admin"] or {}
    return {}


def listen_port(admin: dict) -> int | None:
    listen = str(admin.get("listen") or "")
    if ":" not in listen:
        return None
    _, _, port = listen.rpartition(":")
    try:
        return int(port)
    except ValueError:
        return None


def deployment_admin_ports(documents: list[dict]) -> tuple[set[int], set[str]]:
    numbers: set[int] = set()
    names: set[str] = set()
    for document in documents:
        if document.get("kind") != "Deployment":
            continue
        containers = (((document.get("spec") or {}).get("template") or {}).get("spec") or {}).get("containers") or []
        for container in containers:
            for entry in container.get("ports") or []:
                if entry.get("name") == "admin":
                    if entry.get("containerPort") is not None:
                        numbers.add(int(entry["containerPort"]))
                    names.add("admin")
    return numbers, names


def rule_allows_admin_port(rule: dict, admin_port: int) -> bool:
    """True when the rule names the admin port (by number or container name)."""
    for entry in rule.get("ports") or []:
        port = entry.get("port")
        if port == admin_port or str(port) == "admin":
            return True
    return False


def rule_has_no_ports(rule: dict) -> bool:
    """A rule without `ports` silently allows every port on the pod."""
    return not rule.get("ports")


def ipblock_network(source: dict) -> ipaddress._BaseNetwork | None:
    cidr = str((source.get("ipBlock") or {}).get("cidr") or "")
    try:
        return ipaddress.ip_network(cidr, strict=False)
    except ValueError:
        return None


def broad_source(source: dict, overrides: dict[str, str]) -> str:
    """Return a reason when a NetworkPolicy source is too broad, else ""."""
    if "ipBlock" in source:
        cidr = str((source.get("ipBlock") or {}).get("cidr") or "")
        network = ipblock_network(source)
        if network is None:
            return f"invalid ipBlock {cidr!r}"
        if network.prefixlen == 0:
            return f"ipBlock {cidr} trusts every peer and can never be allowed"
        override = overrides.get(cidr, "")
        if network.version == 4 and network.prefixlen < 16:
            if not override:
                return f"ipBlock {cidr} is wider than /16 and needs a documented override"
            print(f"admin-plane overlay: using documented override for {cidr}: {override}")
        if network.version == 6 and network.prefixlen < 48:
            if not override:
                return f"ipBlock {cidr} is wider than /48 and needs a documented override"
            print(f"admin-plane overlay: using documented override for {cidr}: {override}")
        if not override and network.prefixlen < network.max_prefixlen:
            # A narrower-than-/16 range is still an explicit network exception
            # rather than a labelled pod, so it needs its own documented reason.
            return f"ipBlock {cidr} allows a network instead of an admin-client pod and needs a documented override"
        return ""
    if not source:
        return "empty source allows every peer"
    namespace = source.get("namespaceSelector")
    if namespace is not None and not (namespace.get("matchLabels") or namespace.get("matchExpressions")):
        return "namespaceSelector with no labels allows every namespace"
    if source.get("podSelector") or namespace:
        return ""
    return "source with no selector allows every peer"


def source_is_admin_client(source: dict) -> bool:
    labels = (source.get("podSelector") or {}).get("matchLabels") or {}
    if labels.get(ADMIN_CLIENT_LABEL) != "true":
        return False
    # The selector must also name the namespace: an admin-client label alone in
    # every namespace would let any tenant opt in.
    namespace = source.get("namespaceSelector") or {}
    return bool(namespace.get("matchLabels") or namespace.get("matchExpressions"))


def source_is_admin_client(source: dict) -> bool:
    labels = (source.get("podSelector") or {}).get("matchLabels") or {}
    if labels.get(ADMIN_CLIENT_LABEL) != "true":
        return False
    # The selector must also name the namespace: an admin-client label alone in
    # every namespace would let any tenant opt in.
    namespace = source.get("namespaceSelector") or {}
    return bool(namespace.get("matchLabels") or namespace.get("matchExpressions"))


def check_documents(
    documents: list[dict], problems: Problems, overrides: dict[str, str] | None = None
) -> None:
    overrides = BROAD_IPBLOCK_OVERRIDES if overrides is None else overrides
    admin = admin_config(documents)
    problems.check(bool(admin), "the rendered ConfigMap must carry the admin block")
    problems.check(admin.get("enabled") is True, "admin.enabled must be true in the rendered config")
    port = listen_port(admin)
    problems.check(port is not None, "admin.listen must be a host:port")
    if port is None:
        return

    tls_mode = str((admin.get("tls") or {}).get("mode") or "off").lower()
    listen = str(admin.get("listen") or "")
    loopback = listen.startswith(("127.", "[::1]", "localhost"))
    if not loopback and tls_mode == "off":
        problems.check(
            admin.get("allow_plaintext_non_loopback") is True,
            "a non-loopback plaintext admin listener needs allow_plaintext_non_loopback: true",
        )

    ports, names = deployment_admin_ports(documents)
    problems.check(port in ports, f"the Deployment must expose containerPort {port} named admin")

    services = [doc for doc in documents if doc.get("kind") == "Service" and "admin" in str((doc.get("metadata") or {}).get("name", ""))]
    problems.check(len(services) == 1, "exactly one admin Service is expected")
    for service in services:
        spec = service.get("spec") or {}
        problems.check(str(spec.get("type", "ClusterIP")) == "ClusterIP", "the admin Service must be ClusterIP")
        targets = [entry.get("targetPort") for entry in spec.get("ports") or []]
        problems.check(
            any(target == "admin" or target == port for target in targets),
            "the admin Service must target the admin container port",
        )

    ingresses = [doc for doc in documents if doc.get("kind") == "Ingress"]
    for ingress in ingresses:
        name = (ingress.get("metadata") or {}).get("name", "")
        spec = ingress.get("spec") or {}
        backends = []
        for rule in spec.get("rules") or []:
            for path in ((rule.get("http") or {}).get("paths") or []):
                backends.append(((path.get("backend") or {}).get("service") or {}).get("name", ""))
        backends.append(((spec.get("defaultBackend") or {}).get("service") or {}).get("name", ""))
        problems.check(
            not any("admin" in str(backend) for backend in backends),
            f"Ingress {name} must not select the admin Service",
        )

    policies = [doc for doc in documents if doc.get("kind") == "NetworkPolicy" and router_selector_matches((doc.get("spec") or {}).get("podSelector"))]
    problems.check(bool(policies), "the rendered overlay must ship a NetworkPolicy selecting the router pods")
    deny_all = any(
        "Ingress" in ((policy.get("spec") or {}).get("policyTypes") or [])
        and (policy.get("spec") or {}).get("ingress") == []
        for policy in policies
    )
    problems.check(deny_all, "a default-deny ingress policy must select the router pods")

    admin_allowed_from_selected = False
    for policy in policies:
        for rule in (policy.get("spec") or {}).get("ingress") or []:
            if rule_has_no_ports(rule):
                problems.check(False, "an ingress rule without `ports` allows every port, including the admin port; name the ports explicitly")
                continue
            if not rule_allows_admin_port(rule, port):
                continue
            sources = rule.get("from")
            if not sources:
                problems.check(False, "no NetworkPolicy may allow the admin port from every source (missing or empty `from`)")
                continue
            admin_sources = [source for source in sources if source_is_admin_client(source)]
            other_sources = [source for source in sources if source not in admin_sources]
            if admin_sources and other_sources:
                # A rule that mixes the admin-client selector with anything else
                # widens access for every source it lists.
                problems.check(False, "an admin-port rule must not mix the admin-client selector with another source")
            for source in admin_sources:
                reason = broad_source(source, overrides)
                problems.check(not reason, f"admin port source refused: {reason}")
                admin_allowed_from_selected = True
            for source in other_sources:
                reason = broad_source(source, overrides)
                problems.check(not reason, f"admin port source refused: {reason}")
    problems.check(admin_allowed_from_selected, "the admin port must be allowed from selected admin-client pods")
    _ = names


def main() -> int:
    problems = Problems()
    check_documents(render_overlay(OVERLAY), problems)
    if problems.items:
        for item in problems.items:
            print(f"admin-plane overlay: {item}", file=sys.stderr)
        return 1
    print("admin-plane overlay ok (rendered)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
