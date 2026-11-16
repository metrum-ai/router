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
        assert "useradd --uid 10001" in content, dockerfile
        assert "ENV HOME=/home/harbor" in content, dockerfile
        assert "install -d -o 10001 -g 10001 /app /logs/agent /logs/verifier /run/harbor-e2e" in content, dockerfile
        assert "COPY --chown=10001:10001 workspace/ /app/" in content, dockerfile
        assert "USER 10001:10001" in content, dockerfile

    staging = (ROOT / "scripts/harbor_e2e/prepare_catalog_tasks.py").read_text(encoding="utf-8")
    assert "useradd --uid 10001" in staging
    assert "ENV HOME=/home/harbor" in staging
    assert "install -d -o 10001 -g 10001 /app /logs/agent /logs/verifier /run/harbor-e2e" in staging
    assert "COPY --chown=10001:10001 workspace/ /app/" in staging
    assert "USER 10001:10001" in staging

    synthetic = (ROOT / "scripts/harbor_e2e/generate_tasks.py").read_text(encoding="utf-8")
    assert "useradd --uid 10001" in synthetic
    assert "ENV HOME=/home/harbor" in synthetic
    assert "install -d -o 10001 -g 10001 /app /logs/agent /logs/verifier /run/harbor-e2e" in synthetic
    assert "COPY --chown=10001:10001 workspace/ /app/" in synthetic
    assert "USER 10001:10001" in synthetic
    smoke = (ROOT / "scripts/harbor_e2e/test_nonroot_images.sh").read_text(encoding="utf-8")
    assert "test -r /run/harbor-e2e/router-token" in smoke
    assert "chmod 0777" not in smoke


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
    assert "failureAction: Enforce" in policy
    assert "validationFailureAction" not in policy
    assert "sha256" in policy
    assert "regex_match(" in policy
    assert policy.count("regex_match(") == 3
    assert "operator: Equals" in policy
    assert "value: false" in policy
    assert "(docker\\\\.io/)?vllm/vllm-openai" in policy
    assert "request.object.spec.containers" in policy
    assert "request.object.spec.initContainers" in policy
    assert "request.object.spec.ephemeralContainers" in policy
    assert "Pod/ephemeralcontainers" in policy
    pattern = re.search(r"regex_match\('([^']+)'", policy).group(1).replace("\\\\", "\\")
    allowed = [
        "vllm/vllm-openai:v0.33.0@sha256:" + "a" * 64,
        "docker.io/vllm/vllm-openai-rocm:v0.33.0@sha256:" + "a" * 64,
        "registry.example.com/metrum-ai-router:v5-linux-amd64@sha256:" + "a" * 64,
    ]
    blocked = [
        "docker.io/library/nginx:latest",
        "vllm/vllm-openai:v0.33.0",
        "registry.example.com/metrum-ai-router:v5",
    ]
    assert all(re.fullmatch(pattern, image) for image in allowed)
    assert not any(re.fullmatch(pattern, image) for image in blocked)
    docs = (ROOT / "docs-site/docs/installation/kubernetes.md").read_text(encoding="utf-8")
    assert "https://github.com/metrum-ai/router/blob/main/deploy/kubernetes/trusted-registry-policy.example.yaml" in docs
    assert "../../../deploy/kubernetes/trusted-registry-policy.example.yaml" not in docs


if __name__ == "__main__":
    test_harbor_task_images_run_as_fixed_non_root_user()
    test_kubernetes_images_are_digest_pinned_and_policy_is_opt_in()
