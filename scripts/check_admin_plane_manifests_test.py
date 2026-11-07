#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Unit tests for the rendered admin-plane manifest check (#817).

Every bypass the review found has a negative case: a rule without `ports`, a
rule without `from`, the named port `admin`, a mixed `from` list with a default
route, a non-ClusterIP Service, an Ingress pointed at the admin Service, and a
missing default-deny policy.
"""

from __future__ import annotations

import io
import sys
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_admin_plane_manifests as check

ADMIN_PORT = 8081

CONFIG_MAP = {
    "apiVersion": "v1",
    "kind": "ConfigMap",
    "metadata": {"name": "metrum-ai-router-config"},
    "data": {
        "config.yaml": (
            "admin:\n"
            "  enabled: true\n"
            "  listen: \"0.0.0.0:8081\"\n"
            "  allow_plaintext_non_loopback: true\n"
            "server:\n"
            "  listen: \":8080\"\n"
        )
    },
}

DEPLOYMENT = {
    "apiVersion": "apps/v1",
    "kind": "Deployment",
    "metadata": {"name": "metrum-ai-router"},
    "spec": {
        "template": {
            "spec": {
                "containers": [
                    {
                        "name": "router",
                        "ports": [
                            {"name": "http", "containerPort": 8080},
                            {"name": "admin", "containerPort": ADMIN_PORT},
                        ],
                    }
                ]
            }
        }
    },
}

ADMIN_SERVICE = {
    "apiVersion": "v1",
    "kind": "Service",
    "metadata": {"name": "metrum-ai-router-admin"},
    "spec": {"type": "ClusterIP", "ports": [{"name": "admin", "port": ADMIN_PORT, "targetPort": "admin"}]},
}

DENY_ALL = {
    "apiVersion": "networking.k8s.io/v1",
    "kind": "NetworkPolicy",
    "metadata": {"name": "metrum-ai-router-default-deny-ingress"},
    "spec": {
        "podSelector": {"matchLabels": {"app.kubernetes.io/name": "metrum-ai-router"}},
        "policyTypes": ["Ingress"],
        "ingress": [],
    },
}


def allow_policy(ingress: list[dict]) -> dict:
    return {
        "apiVersion": "networking.k8s.io/v1",
        "kind": "NetworkPolicy",
        "metadata": {"name": "metrum-ai-router-serving-and-selected-admin"},
        "spec": {
            "podSelector": {"matchLabels": {"app.kubernetes.io/name": "metrum-ai-router"}},
            "policyTypes": ["Ingress"],
            "ingress": ingress,
        },
    }


SELECTED_FROM = [
    {
        "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "metrum-ai-router"}},
        "podSelector": {"matchLabels": {"access.metrum.ai/admin-client": "true"}},
    }
]


def healthy(admin_rule: list[dict] | None = None) -> list[dict]:
    return [
        CONFIG_MAP,
        DEPLOYMENT,
        ADMIN_SERVICE,
        DENY_ALL,
        allow_policy(
            admin_rule
            if admin_rule is not None
            else [{"ports": [{"protocol": "TCP", "port": 8080}]}, {"from": SELECTED_FROM, "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]
        ),
    ]


class CheckRenderedManifestsTest(unittest.TestCase):
    def run_check(self, documents: list[dict], overrides: dict[str, str] | None = None) -> tuple[int, str]:
        problems = check.Problems()
        check.check_documents(documents, problems, overrides)
        return (1 if problems.items else 0), "\n".join(problems.items)

    def assert_rejected(self, documents: list[dict], needle: str) -> None:
        code, output = self.run_check(documents)
        self.assertEqual(code, 1, output)
        self.assertIn(needle, output)


    def test_namespace_selector_without_labels_is_refused(self) -> None:
        source = {
            "namespaceSelector": {},
            "podSelector": {"matchLabels": {"access.metrum.ai/admin-client": "true"}},
        }
        self.assert_rejected(
            healthy([{"from": [source], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]),
            "namespaceSelector with no labels allows every namespace",
        )

    def test_admin_client_label_in_every_namespace_is_refused(self) -> None:
        # The admin-client label alone, without a namespace match, must not count.
        source = {"podSelector": {"matchLabels": {"access.metrum.ai/admin-client": "true"}}}
        self.assert_rejected(
            healthy([{"from": [source], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]),
            "allowed from selected admin-client pods",
        )

    def test_mixed_admin_client_and_other_source_is_refused(self) -> None:
        source = {"podSelector": {"matchLabels": {"app": "bastion"}}}
        self.assert_rejected(
            healthy([{"from": SELECTED_FROM + [source], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]),
            "must not mix the admin-client selector with another source",
        )

    def test_mixed_admin_client_and_ipblock_is_refused(self) -> None:
        source = {"ipBlock": {"cidr": "10.1.0.0/24"}}
        self.assert_rejected(
            healthy([{"from": SELECTED_FROM + [source], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]),
            "must not mix the admin-client selector with another source",
        )

    def test_wide_ipv4_ipblock_needs_a_documented_override(self) -> None:
        rule = [{"from": [{"ipBlock": {"cidr": "10.0.0.0/8"}}], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]
        self.assert_rejected(healthy(rule), "wider than /16")

    def test_wide_ipv6_ipblock_needs_a_documented_override(self) -> None:
        rule = [{"from": [{"ipBlock": {"cidr": "2001:db8::/32"}}], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]
        self.assert_rejected(healthy(rule), "wider than /48")

    def test_bastion_network_needs_a_documented_override(self) -> None:
        rule = [{"from": [{"ipBlock": {"cidr": "10.1.0.0/24"}}], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]
        self.assert_rejected(healthy(rule), "needs a documented override")

    def test_documented_override_allows_a_bastion_network(self) -> None:
        rule = [{"from": [{"ipBlock": {"cidr": "10.1.0.0/24"}}], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]
        documents = healthy(rule)
        # An explicitly documented bastion range carries its reason and still
        # needs the admin-client selector somewhere for the normal path, so the
        # healthy rule set keeps both an override and the selector.
        documents = [doc for doc in documents if doc.get("metadata", {}).get("name") != "metrum-ai-router-serving-and-selected-admin"]
        documents.append(allow_policy([{"from": SELECTED_FROM, "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]))
        documents.append(allow_policy(rule))
        problems = check.Problems()
        check.check_documents(documents, problems, {"10.1.0.0/24": "reviewed bastion subnet, ticket OPS-1234"})
        self.assertEqual(problems.items, [])

    def test_healthy_overlay_passes(self) -> None:
        code, output = self.run_check(healthy())
        self.assertEqual(code, 0, output)

    def test_rule_without_ports_allows_every_port(self) -> None:
        # A port-less rule silently covers the admin port as well.
        self.assert_rejected(
            healthy([{"from": SELECTED_FROM}]),
            "ingress rule without `ports` allows every port",
        )

    def test_rule_without_from_allows_every_source(self) -> None:
        self.assert_rejected(
            healthy([{"ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]),
            "no NetworkPolicy may allow the admin port from every source",
        )

    def test_empty_from_allows_every_source(self) -> None:
        self.assert_rejected(
            healthy([{"from": [], "ports": [{"protocol": "TCP", "port": ADMIN_PORT}]}]),
            "no NetworkPolicy may allow the admin port from every source",
        )

    def test_empty_rule_object_allows_everything(self) -> None:
        self.assert_rejected(healthy([{}]), "ingress rule without `ports` allows every port")

    def test_named_admin_port_is_resolved(self) -> None:
        # A broad source on the named port must be refused, not ignored.
        self.assert_rejected(
            healthy([{"ports": [{"protocol": "TCP", "port": "admin"}]}]),
            "no NetworkPolicy may allow the admin port from every source",
        )

    def test_mixed_from_with_default_route_is_refused(self) -> None:
        self.assert_rejected(
            healthy(
                [
                    {
                        "from": SELECTED_FROM + [{"ipBlock": {"cidr": "0.0.0.0/0"}}],
                        "ports": [{"protocol": "TCP", "port": ADMIN_PORT}],
                    }
                ]
            ),
            "ipBlock 0.0.0.0/0 trusts every peer",
        )

    def test_wide_ipv4_ip_block_is_refused(self) -> None:
        self.assert_rejected(
            healthy(
                [
                    {
                        "from": [{"ipBlock": {"cidr": "10.0.0.0/7"}}],
                        "ports": [{"protocol": "TCP", "port": ADMIN_PORT}],
                    }
                ]
            ),
            "wider than /16",
        )

    def test_selector_without_admin_label_is_not_enough(self) -> None:
        self.assert_rejected(
            healthy(
                [
                    {
                        "from": [{"podSelector": {"matchLabels": {"app": "something-else"}}}],
                        "ports": [{"protocol": "TCP", "port": ADMIN_PORT}],
                    }
                ]
            ),
            "must be allowed from selected admin-client pods",
        )

    def test_node_port_admin_service_is_refused(self) -> None:
        documents = healthy()
        for document in documents:
            if document is ADMIN_SERVICE:
                document = dict(document)
        documents = [dict(doc) for doc in documents]
        for document in documents:
            if document.get("kind") == "Service":
                document["spec"] = dict(document["spec"])
                document["spec"]["type"] = "NodePort"
        self.assert_rejected(documents, "the admin Service must be ClusterIP")

    def test_ingress_selecting_admin_is_refused(self) -> None:
        ingress = {
            "apiVersion": "networking.k8s.io/v1",
            "kind": "Ingress",
            "metadata": {"name": "example"},
            "spec": {
                "rules": [
                    {
                        "http": {
                            "paths": [
                                {"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "metrum-ai-router-admin", "port": {"number": ADMIN_PORT}}}}
                            ]
                        }
                    }
                ]
            },
        }
        self.assert_rejected(healthy() + [ingress], "must not select the admin Service")

    def test_default_backend_to_admin_is_refused(self) -> None:
        ingress = {
            "apiVersion": "networking.k8s.io/v1",
            "kind": "Ingress",
            "metadata": {"name": "example"},
            "spec": {"defaultBackend": {"service": {"name": "metrum-ai-router-admin", "port": {"number": ADMIN_PORT}}}},
        }
        self.assert_rejected(healthy() + [ingress], "must not select the admin Service")

    def test_missing_default_deny_is_refused(self) -> None:
        documents = [doc for doc in healthy() if doc is not DENY_ALL]
        self.assert_rejected(documents, "default-deny ingress policy")

    def test_unacknowledged_plaintext_bind_is_refused(self) -> None:
        documents = [dict(doc) for doc in healthy()]
        for document in documents:
            if document.get("kind") == "ConfigMap":
                document["data"] = {
                    "config.yaml": "admin:\n  enabled: true\n  listen: \"0.0.0.0:8081\"\nserver:\n  listen: \":8080\"\n"
                }
        self.assert_rejected(documents, "allow_plaintext_non_loopback")

    def test_missing_admin_container_port_is_refused(self) -> None:
        documents = [dict(doc) for doc in healthy()]
        for document in documents:
            if document.get("kind") == "Deployment":
                document["spec"] = {
                    "template": {"spec": {"containers": [{"name": "router", "ports": [{"name": "http", "containerPort": 8080}]}]}}
                }
        self.assert_rejected(documents, "must expose containerPort")

    def test_repository_overlay_renders_and_passes_when_kustomize_exists(self) -> None:
        import shutil

        if shutil.which("kustomize") is None:
            self.skipTest("kustomize is not installed in this environment")
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = check.main()
        self.assertEqual(code, 0, err.getvalue())


if __name__ == "__main__":
    unittest.main()
