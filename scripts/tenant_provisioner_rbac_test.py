#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Static least-privilege checks for the tenant provisioner RBAC template.

Mirrors the `kubectl auth can-i` verification matrix from
docs/EKS_IDENTITY_BOOTSTRAP.md for environments without a cluster: the Role
must stay namespace-scoped, grant no wildcard verbs/resources/apiGroups, touch
no Secrets, and write only the resources the tenant bootstrap templates create.
"""

from __future__ import annotations

import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "deploy" / "kubernetes" / "bootstrap" / "tenant-provisioner-rbac.yaml"

# Approved permission matrix: apiGroup -> resource -> allowed verbs.
APPROVED = {
    ("", "resourcequotas"): {"get", "list", "watch", "create", "update", "patch"},
    ("", "limitranges"): {"get", "list", "watch", "create", "update", "patch"},
    ("networking.k8s.io", "ingresses"): {"get", "list", "watch"},
    ("policy.linkerd.io", "servers"): {"get", "list", "watch", "create", "update", "patch"},
    ("policy.linkerd.io", "serverauthorizations"): {"get", "list", "watch", "create", "update", "patch"},
}

# The tenant default-deny NetworkPolicy is applied once at namespace creation
# by the platform administrator (tenant-default-network-policy.example.yaml,
# Refs #414). The provisioner must hold no NetworkPolicy write access
# (KSV-0056); read-only access is also unnecessary because discovery uses a
# separate read-only binding.
NO_NETWORKPOLICY_ACCESS = ("networking.k8s.io", "networkpolicies")

FORBIDDEN_RESOURCES = {"secrets", "pods", "deployments", "serviceaccounts", "roles", "rolebindings", "clusterroles", "clusterrolebindings"}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def main() -> int:
    docs = [d for d in yaml.safe_load_all(MANIFEST.read_text(encoding="utf-8")) if d]
    by_kind = {}
    for doc in docs:
        by_kind.setdefault(doc.get("kind"), []).append(doc)

    require(len(by_kind.get("ServiceAccount", [])) == 1, "expected exactly one ServiceAccount")
    require(len(by_kind.get("Role", [])) == 1, "expected exactly one Role")
    require("ClusterRole" not in by_kind and "ClusterRoleBinding" not in by_kind, "cluster-scoped RBAC is forbidden")
    require(len(by_kind.get("RoleBinding", [])) == 1, "expected exactly one RoleBinding")

    sa = by_kind["ServiceAccount"][0]
    require(sa["metadata"]["name"] == "tenant-provisioner", "unexpected service account name")

    role = by_kind["Role"][0]
    binding = by_kind["RoleBinding"][0]
    for obj, label in ((sa, "ServiceAccount"), (role, "Role"), (binding, "RoleBinding")):
        ns = obj["metadata"].get("namespace", "")
        require(bool(ns) and ns != "default", f"{label} must pin an explicit non-default namespace placeholder")

    require(binding["roleRef"]["kind"] == "Role", "RoleBinding must reference a Role, never a ClusterRole")
    require(binding["roleRef"]["name"] == role["metadata"]["name"], "RoleBinding must reference the tenant-bootstrap-provisioner Role")
    subjects = binding.get("subjects", [])
    require(len(subjects) == 1, "RoleBinding must bind exactly one subject")
    subject = subjects[0]
    require(subject.get("kind") == "ServiceAccount" and subject.get("name") == "tenant-provisioner", "RoleBinding must bind only the tenant-provisioner ServiceAccount")
    require(subject.get("namespace") == binding["metadata"]["namespace"], "subject namespace must match the binding namespace (no cross-namespace bind)")

    seen: dict[tuple[str, str], set[str]] = {}
    for rule in role.get("rules", []):
        for group in rule.get("apiGroups", []):
            require(group != "*", "wildcard apiGroup is forbidden")
            for resource in rule.get("resources", []):
                require(resource != "*", "wildcard resource is forbidden")
                require(resource.lower() not in FORBIDDEN_RESOURCES, f"forbidden resource in Role: {resource}")
                key = (group, resource)
                require(key in APPROVED, f"unapproved resource grant: {group}/{resource}")
                verbs = set(rule.get("verbs", []))
                require("*" not in verbs, "wildcard verb is forbidden")
                require("delete" not in verbs, "delete is forbidden: teardown removes the tenant namespace")
                require(verbs <= APPROVED[key], f"verbs {sorted(verbs - APPROVED[key])} exceed the approved matrix for {group}/{resource}")
                seen.setdefault(key, set()).update(verbs)

    for key, verbs in APPROVED.items():
        require(key in seen, f"approved grant missing from Role: {key[0]}/{key[1]}")
        require(seen[key] >= verbs, f"Role lost required verbs for {key[0]}/{key[1]}: missing {sorted(verbs - seen[key])}")

    require(NO_NETWORKPOLICY_ACCESS not in seen, "provisioner must have no NetworkPolicy access (KSV-0056, Refs #414)")

    bootstrap = ROOT / "deploy" / "kubernetes" / "bootstrap"
    require((bootstrap / "tenant-default-network-policy.example.yaml").exists(),
            "tenant-default-network-policy.example.yaml must exist: the default-deny policy is applied by the platform administrator at namespace creation, not by the provisioner (Refs #414)")
    bootstrap_docs = [d for d in yaml.safe_load_all((bootstrap / "tenant-bootstrap-resources.example.yaml").read_text(encoding="utf-8")) if d]
    require(all(d.get("kind") != "NetworkPolicy" for d in bootstrap_docs),
            "tenant-bootstrap-resources.example.yaml must not create NetworkPolicy objects (Refs #414)")

    print(f"ok: tenant provisioner Role matches the approved least-privilege matrix ({len(seen)} resource grants)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except AssertionError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
