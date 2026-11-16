# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]


def test_harbor_task_images_run_as_fixed_non_root_user():
    dockerfiles = sorted((ROOT / "tests/harbor/tasks").glob("HARBOR-*/environment/Dockerfile"))
    assert len(dockerfiles) == 6
    for dockerfile in dockerfiles:
        content = dockerfile.read_text(encoding="utf-8")
        assert "COPY --chown=10001:10001 workspace/ /app/" in content, dockerfile
        assert "USER 10001:10001" in content, dockerfile

    staging = (ROOT / "scripts/harbor_e2e/prepare_catalog_tasks.py").read_text(encoding="utf-8")
    assert "COPY --chown=10001:10001 workspace/ /app/" in staging
    assert "USER 10001:10001" in staging


def test_kubernetes_images_are_digest_pinned_and_policy_is_opt_in():
    files = sorted((ROOT / "deploy/kubernetes").rglob("*.yaml"))
    images = []
    for path in files:
        for line in path.read_text(encoding="utf-8").splitlines():
            match = re.match(r"\s*(?:image|router_image):\s*(\S+)", line)
            if match:
                images.append((path, match.group(1)))
    assert images
    for path, image in images:
        if "registry.example.com/metrum-ai-router:" in image or "vllm/" in image:
            assert "@sha256:" in image, (path, image)

    policy = (ROOT / "deploy/kubernetes/trusted-registry-policy.example.yaml").read_text(encoding="utf-8")
    assert "validationFailureAction: Enforce" in policy
    assert "sha256" in policy
    assert "metrum-ai-router|docker" in policy
    assert "vllm-openai" in policy
    assert "trusted-registry-policy.example.yaml" in (
        ROOT / "docs-site/docs/installation/kubernetes.md"
    ).read_text(encoding="utf-8")


if __name__ == "__main__":
    test_harbor_task_images_run_as_fixed_non_root_user()
    test_kubernetes_images_are_digest_pinned_and_policy_is_opt_in()
