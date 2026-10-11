#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Self-test the outcome-calibrated external policy reference."""

from __future__ import annotations

import importlib.util
import json
import random
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
EXAMPLE_DIR = ROOT / "examples" / "external-routing-policy"
SCRIPT = EXAMPLE_DIR / "outcome_calibrated_policy.py"
DATASET = EXAMPLE_DIR / "outcome_dataset.json"
REVIEWS = EXAMPLE_DIR / "reviewed_outcomes.jsonl"


def load_module():
    spec = importlib.util.spec_from_file_location("outcome_policy", SCRIPT)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class FakeEmbeddings:
    vectors = {
        "What is 2+2?": [1.0, 0.0, 0.0],
        "Create a runnable load-testing benchmark for HTTP servers.": [0.0, 1.0, 0.0],
        "Show a folder listing.": [0.0, 0.0, 1.0],
        "What is 2+2? Reply with only the answer.": [1.0, 0.0, 0.0],
        "Create a runnable load-testing benchmark for HTTP servers.": [0.0, 1.0, 0.0],
        "Show a folder listing.": [0.0, 0.0, 1.0],
        "Write a poem about a moonlit river.": [-1.0, 0.0, 0.0],
    }

    def embed(self, texts):
        return [self.vectors[text] for text in texts]


def require(value: bool, message: str) -> None:
    if not value:
        raise AssertionError(message)


def request(text: str) -> dict:
    return {
        "targets": [
            {"provider": "mock", "model": "cheap-text", "weight": 50},
            {"provider": "mock", "model": "strong-code", "weight": 50},
        ],
        "request": {"messages": [{"role": "user", "content": text}]},
    }


# The pre-hardening marker regexes, kept here only as a differential oracle for
# the linear Metrum AI marker scanner on short inputs.
LEGACY_STRIP = re.compile(r"\[\[outcome-(?:calibration|live-demo):[^\]\r\n]+\]\]")
LEGACY_CALIBRATION = re.compile(r"\[\[outcome-calibration:([^\]\r\n]+)\]\]")
LEGACY_LIVE_DEMO = re.compile(r"\[\[outcome-live-demo:([^\]\r\n]+)\]\]")


def legacy_value(pattern: re.Pattern[str], text: str) -> str:
    match = pattern.search(text)
    return match.group(1).strip() if match else ""


def marker_request(text: str) -> dict:
    return {"messages": [{"role": "system", "content": text}]}


def check_marker_parsing(module) -> None:
    valid = "[[outcome-calibration: case-7 ]] What is 2+2?"
    require(module.calibration_case_id(marker_request(valid)) == "case-7", "valid calibration marker not parsed")
    require(module.classification_text(marker_request(valid)) == "What is 2+2?", "valid calibration marker not stripped")
    demo = "before [[outcome-live-demo:demo-1]] after [[outcome-calibration:c2]] end"
    require(module.live_demo_case_id(marker_request(demo)) == "demo-1", "valid live-demo marker not parsed")
    require(module.calibration_case_id(marker_request(demo)) == "c2", "second marker kind not parsed")
    require(module.classification_text(marker_request(demo)) == "before after end", "both marker kinds not stripped")

    malformed = [
        "[[outcome-calibration:]] empty value",
        "[[outcome-calibration:case-1] single close",
        "[[outcome-calibration:case\n-1]] newline in value",
        "[[outcome-calibration:case\r-1]] carriage return in value",
        "[[outcome-other:case-1]] unknown kind",
        "[[outcome-calibration case-1]] missing colon",
        "[[outcome-calibration:unterminated",
        "[[outcome-live-demo:unterminated [[outcome-live-demo:",
    ]
    for text in malformed:
        require(module.calibration_case_id(marker_request(text)) == "", f"malformed marker parsed: {text!r}")
        require(module.live_demo_case_id(marker_request(text)) == "", f"malformed marker parsed: {text!r}")
        require(module.strip_markers(text) == text, f"malformed marker stripped: {text!r}")

    # Differential check against the legacy regexes, including nested and
    # overlapping prefixes, on inputs short enough for backtracking to be cheap.
    rng = random.Random(20261011)
    atoms = ["[[outcome-calibration:", "[[outcome-live-demo:", "[[outcome-", "[[", "]]", "]", "[", "a", "b", " ", "\n", "\r", ":"]
    for _ in range(5000):
        text = "".join(rng.choice(atoms) for _ in range(rng.randint(0, 12)))
        require(module.strip_markers(text) == LEGACY_STRIP.sub("", text), f"strip mismatch: {text!r}")
        require(module.marker_value(text, "calibration") == legacy_value(LEGACY_CALIBRATION, text), f"calibration mismatch: {text!r}")
        require(module.marker_value(text, "live-demo") == legacy_value(LEGACY_LIVE_DEMO, text), f"live-demo mismatch: {text!r}")

    adversarial = [
        "[[outcome-calibration:" + "a" * 100000,
        "[[outcome-calibration:" * 20000,
        "[[outcome-live-demo:" * 20000,
        "[[outcome-calibration:\\" * 20000,
    ]
    for text in adversarial:
        started = time.perf_counter()
        module.calibration_case_id(marker_request(text))
        module.live_demo_case_id(marker_request(text))
        module.classification_text(marker_request(text))
        elapsed = time.perf_counter() - started
        require(elapsed < 0.5, f"marker parsing took {elapsed:.3f}s on a {len(text)}-character adversarial input")


def main() -> int:
    module = load_module()
    check_marker_parsing(module)
    dataset = json.loads(DATASET.read_text(encoding="utf-8"))
    reviews = module.read_jsonl(REVIEWS)
    profile = module.reviewed_profile(dataset, reviews)
    require(profile["promotable"], f"sample profile is not promotable: {profile}")
    embeddings = FakeEmbeddings()
    vectors = module.class_vectors(profile, embeddings)

    math = module.policy_decision(profile, request("What is 2+2? Reply with only the answer."), vectors, embeddings)
    code = module.policy_decision(profile, request("Create a runnable load-testing benchmark for HTTP servers."), vectors, embeddings)
    listing = module.policy_decision(profile, request("Show a folder listing."), vectors, embeddings)
    unknown = module.policy_decision(profile, request("Write a poem about a moonlit river."), vectors, embeddings)
    require(math["targetIndex"] == 0, f"arithmetic did not select cheap candidate: {math}")
    require(code["targetIndex"] == 1, f"coding did not select strong candidate: {code}")
    require(listing["targetIndex"] == 0, f"listing did not select cheap candidate: {listing}")
    require(unknown["targetIndex"] == 1 and unknown["classLabel"] == "outcome-policy:unclassified", f"unknown did not select strong default: {unknown}")
    try:
        module.policy_decision(profile, {"targets": request("Show a folder listing.")["targets"]}, vectors, embeddings)
    except ValueError as error:
        require("include_request" in str(error), f"unexpected trusted-content failure: {error}")
    else:
        raise AssertionError("policy accepted a request without include_request mirror")

    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        profile_path = root / "profile.json"
        patch_path = root / "weights.yaml"
        calibrated = subprocess.run([sys.executable, str(SCRIPT), "calibrate", "--dataset", str(DATASET), "--reviews", str(REVIEWS), "--out-profile", str(profile_path), "--out-yaml", str(patch_path)], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        require(calibrated.returncode == 0, f"calibration failed: {calibrated.stdout}\n{calibrated.stderr}")
        require("strategy: external" in patch_path.read_text(encoding="utf-8"), "missing external strategy patch")
        overrides = root / "overrides.json"
        planned = subprocess.run([sys.executable, str(SCRIPT), "plan", "--dataset", str(DATASET), "--out-overrides", str(overrides)], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        require(planned.returncode == 0, f"calibration plan failed: {planned.stdout}\n{planned.stderr}")
        require(len(json.loads(overrides.read_text(encoding="utf-8"))["calibrationOverrides"]) == 6, "calibration plan did not cover every case/candidate")
        insufficient = root / "insufficient.jsonl"
        insufficient.write_text(REVIEWS.read_text(encoding="utf-8").splitlines()[0] + "\n", encoding="utf-8")
        failed = subprocess.run([sys.executable, str(SCRIPT), "calibrate", "--dataset", str(DATASET), "--reviews", str(insufficient), "--out-profile", str(root / "failed.json"), "--out-yaml", str(root / "failed.yaml")], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        require(failed.returncode == 2 and "insufficient-evidence" in failed.stdout, f"insufficient evidence was promoted: {failed.stdout}\n{failed.stderr}")

    print("outcome-calibrated policy self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
