# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError, URLError

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine_advisory_gate as gate


class ImageVersionTests(unittest.TestCase):
    def test_extracts_rocm_vllm_version(self):
        image = "rocm/vllm:rocm7.14.1_cdna_ubuntu24.04_py3.14_pytorch_2.11_vllm_0.23.0@sha256:abc"
        self.assertEqual(gate.version_from_reference(image), "0.23.0")

    def test_extracts_release_tag_version(self):
        self.assertEqual(gate.version_from_reference("vllm/vllm-openai:v0.33.0"), "0.33.0")

    def test_rejects_moving_tag(self):
        with self.assertRaisesRegex(gate.GateError, "could not resolve"):
            gate.version_from_reference("vllm/vllm-openai:latest")

    def test_resolves_compose_image_default(self):
        self.assertEqual(
            gate.resolve_variable_default("vllm/vllm-openai:${VLLM_VERSION:-v0.33.0}"),
            "vllm/vllm-openai:v0.33.0",
        )


class ExceptionTests(unittest.TestCase):
    def test_exception_requires_exact_advisory_and_image(self):
        with tempfile.TemporaryDirectory() as temp:
            readme = Path(temp) / "README.md"
            reference = "vllm/vllm-openai-rocm:v0.33.0@sha256:" + "a" * 64
            item = {"advisory": "GHSA-v97p-wpxp-px84", "image": "vllm/vllm-openai-rocm:v0.33.0", "reason": "This tier uses pipeline parallelism one."}
            readme.write_text("<!-- engine-advisory-exception " + json.dumps(item) + " -->\n", encoding="utf-8")
            key = ("GHSA-v97p-wpxp-px84", "vllm/vllm-openai-rocm", "0.33.0")
            self.assertEqual(gate.exceptions_in(readme)[key], item["reason"])
            self.assertEqual(gate.image_identity(reference), key[1:])

    def test_malformed_exception_fails_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            readme = Path(temp) / "README.md"
            readme.write_text("<!-- engine-advisory-exception {bad} -->\n", encoding="utf-8")
            with self.assertRaisesRegex(gate.GateError, "malformed"):
                gate.exceptions_in(readme)

    def test_missing_exact_exception_fails_gate(self):
        image = gate.EngineImage(gate.ROOT / "deploy/reference/amd-mi350p-datacenter/compose.yaml",
                                 "rocm/vllm:rocm7.13.0_vllm_0.19.1", "vllm", "vllm", "0.19.1")
        with tempfile.TemporaryDirectory() as temp:
            readme = Path(temp) / "README.md"
            readme.write_text("<!-- engine-advisory-exception {\"advisory\":\"GHSA-test\",\"image\":\"vllm/vllm-openai-rocm:v0.18.0\",\"reason\":\"documented config limit applies\"} -->\n", encoding="utf-8")
            with patch.object(gate, "nearest_readme", return_value=readme), \
                 patch.object(gate, "github_advisories", return_value=[{"ghsa_id": "GHSA-test"}]), \
                 patch.object(gate, "osv_advisories", return_value=[]):
                result = gate.run_gate([image])
        self.assertEqual(result["unexcepted"], 1)

    def test_exact_exception_allows_documented_match_from_fixture_deploy_root(self):
        with tempfile.TemporaryDirectory() as temp:
            deploy = Path(temp) / "deploy"
            tier = deploy / "reference" / "amd-mi350p-datacenter"
            tier.mkdir(parents=True)
            source = tier / "compose.yaml"
            reference = "vllm/vllm-openai-rocm:v0.33.0@sha256:" + "a" * 64
            image = gate.EngineImage(source, reference, "vllm", "vllm", "0.33.0")
            item = {"advisory": "GHSA-test", "image": reference, "reason": "This exact tier disables the affected feature."}
            (tier / "README.md").write_text("<!-- engine-advisory-exception " + json.dumps(item) + " -->\n", encoding="utf-8")
            with patch.object(gate, "github_advisories", return_value=[{"ghsa_id": "GHSA-test"}]), \
                 patch.object(gate, "osv_advisories", return_value=[]):
                result = gate.run_gate([image], deploy_root=deploy)
        self.assertEqual(result["unexcepted"], 0)


class DeploymentTreeTests(unittest.TestCase):
    def make_deploy_tree(self, root: Path, image: str) -> Path:
        deploy = root / "deploy"
        (deploy / "kubernetes/overlays/test/serving").mkdir(parents=True)
        (deploy / "kubernetes/intents").mkdir(parents=True)
        (deploy / "kubernetes/overlays/test/serving/vllm-deployment.yaml").write_text(
            "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - name: engine\n          image: " + image + "\n",
            encoding="utf-8",
        )
        (deploy / "kubernetes/intents/model.example.yaml").write_text(
            "models:\n  - image: " + image + "\n", encoding="utf-8"
        )
        return deploy

    def run_check(self, deploy: Path, github: list[dict[str, str]], snapshot: Path | None = None) -> int:
        argv = ["engine_advisory_gate.py", "--check"]
        if snapshot is not None:
            argv.extend(["--snapshot", str(snapshot)])
        with patch.object(gate, "DEPLOY", deploy), patch.object(sys, "argv", argv), \
             patch.object(gate, "github_advisories", return_value=github), \
             patch.object(gate, "osv_advisories", return_value=[]):
            return gate.main()

    def test_enumerates_all_yaml_names_under_deploy(self):
        with tempfile.TemporaryDirectory() as temp:
            deploy = self.make_deploy_tree(Path(temp), "vllm/vllm-openai:v0.33.0")
            images = gate.enumerate_images(deploy)
        self.assertEqual(len(images), 2)
        self.assertTrue(any(image.source.name == "vllm-deployment.yaml" for image in images))
        self.assertTrue(any(image.source.name == "model.example.yaml" for image in images))

    def test_main_check_fails_for_vulnerable_image_in_temporary_deploy(self):
        with tempfile.TemporaryDirectory() as temp:
            deploy = self.make_deploy_tree(Path(temp), "vllm/vllm-openai:v0.11.0")
            result = self.run_check(deploy, [{"ghsa_id": "GHSA-fixture"}])
        self.assertEqual(result, 1)

    def test_main_check_passes_for_clean_image_in_temporary_deploy(self):
        with tempfile.TemporaryDirectory() as temp:
            deploy = self.make_deploy_tree(Path(temp), "vllm/vllm-openai:v0.33.0")
            result = self.run_check(deploy, [])
        self.assertEqual(result, 0)

    def test_main_writes_snapshot_with_all_pins_and_findings(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            deploy = self.make_deploy_tree(root, "vllm/vllm-openai:v0.11.0")
            snapshot = root / "dist/engine-advisories.json"
            result = self.run_check(deploy, [{"ghsa_id": "GHSA-fixture"}], snapshot)
            payload = json.loads(snapshot.read_text(encoding="utf-8"))
        self.assertEqual(result, 1)
        self.assertEqual(payload["checked_images"], 2)
        self.assertEqual(len(payload["images"]), 2)
        self.assertEqual(payload["advisories"], 2)
        self.assertTrue(all(item["image"] == "vllm/vllm-openai:v0.11.0" for item in payload["images"]))


class NetworkFailureTests(unittest.TestCase):
    def test_too_many_images_fails_closed_before_network(self):
        image = gate.EngineImage(gate.ROOT / "deploy/edge/compose.amd.yaml", "vllm/vllm-openai-rocm:v0.33.0", "vllm", "vllm", "0.33.0")
        images = [image] * (gate.MAX_ENGINE_IMAGES + 1)
        with patch.object(gate, "github_advisories") as github:
            with self.assertRaisesRegex(gate.GateError, "too many engine image references"):
                gate.run_gate(images)
        github.assert_not_called()

    def test_http_failure_reports_status_without_bypassing(self):
        failure = HTTPError(gate.GITHUB_ADVISORIES, 403, "rate limited", {}, None)
        with patch.object(gate.urllib.request, "urlopen", side_effect=failure):
            with self.assertRaisesRegex(gate.GateError, "HTTP 403"):
                gate.github_advisories("vllm", "0.33.0")

    def test_github_request_uses_token_and_retries_transient_failure_once(self):
        failure = HTTPError(gate.GITHUB_ADVISORIES, 503, "unavailable", {"Retry-After": "8"}, None)

        class Response:
            status = 200
            headers = {}

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self, _limit):
                return b"[]"

        with patch.dict("os.environ", {"GITHUB_TOKEN": "test-token"}), \
             patch.object(gate.urllib.request, "urlopen", side_effect=[failure, Response()]) as opener, \
             patch.object(gate.time, "sleep") as sleep:
            self.assertEqual(gate.github_advisories("vllm", "0.33.0"), [])
        self.assertEqual(opener.call_count, 2)
        self.assertEqual(opener.call_args_list[0].args[0].get_header("Authorization"), "Bearer test-token")
        sleep.assert_called_once_with(gate.MAX_RETRY_DELAY_SECONDS)

    def test_timeout_fails_closed(self):
        with patch.object(gate.urllib.request, "urlopen", side_effect=TimeoutError):
            with self.assertRaisesRegex(gate.GateError, "failed or timed out"):
                gate.request_json(gate.OSV_QUERY, {"package": {"name": "vllm", "ecosystem": "PyPI"}, "version": "0.19.1"})

    def test_oversized_api_response_fails_closed(self):
        class Response:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self, limit):
                return b"x" * limit

        with patch.object(gate.urllib.request, "urlopen", return_value=Response()):
            with self.assertRaisesRegex(gate.GateError, "size limit"):
                gate.request_json(gate.OSV_QUERY, {"package": {"name": "vllm", "ecosystem": "PyPI"}, "version": "0.33.0"})

    def test_connection_failure_fails_closed(self):
        with patch.object(gate.urllib.request, "urlopen", side_effect=URLError("offline")):
            with self.assertRaisesRegex(gate.GateError, "failed or timed out"):
                gate.github_advisories("vllm", "0.19.1")


if __name__ == "__main__":
    unittest.main()
