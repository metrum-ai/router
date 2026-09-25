#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for OpenJev policy helpers (no network)."""
from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
POLICY = ROOT / "examples" / "external-routing-policy" / "openjev_policy.py"


def load_policy():
    spec = importlib.util.spec_from_file_location("openjev_policy", POLICY)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class OpenJevPolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.m = load_policy()

    def test_extract_text_from_messages(self) -> None:
        text = self.m.extract_text(
            {
                "request": {
                    "system": "Be brief.",
                    "messages": [{"role": "user", "content": "Summarize this."}],
                }
            }
        )
        self.assertIn("Summarize", text)
        self.assertIn("Be brief", text)

    def test_parse_and_map_simple(self) -> None:
        parsed = self.m.parse_openjev_answers(
            {
                "answers": {
                    "task": {
                        "choice": "simple",
                        "confidence": 0.91,
                        "probabilities": {"simple": 0.91, "medium": 0.05, "advanced": 0.04},
                    },
                    "complexity": {"score": 0.5},
                    "high_risk": {"probability": 0.05},
                }
            }
        )
        targets = [
            {"provider": "openai", "model": "gpt-5.6-luna", "tier": "cheap", "keyConfigured": True},
            {"provider": "openai", "model": "gpt-5.6-sol", "tier": "medium", "keyConfigured": True},
            {"provider": "openai", "model": "gpt-6-astra", "tier": "advanced", "keyConfigured": True},
        ]
        decision = self.m.decide_from_parsed(
            {"targets": targets}, parsed, confidence_floor=0.45
        )
        self.assertEqual(decision["targetIndex"], 0)
        self.assertEqual(decision["classLabel"], "openjev:simple")
        self.assertFalse(decision["metadata"]["escalated"])

    def test_low_confidence_escalates(self) -> None:
        parsed = {
            "task": "simple",
            "confidence": 0.2,
            "complexity": 1.0,
            "high_risk": False,
        }
        targets = [
            {"model": "gpt-5.6-luna", "tier": "cheap", "keyConfigured": True},
            {"model": "gpt-5.6-sol", "tier": "medium", "keyConfigured": True},
            {"model": "gpt-6-astra", "tier": "advanced", "keyConfigured": True},
        ]
        decision = self.m.decide_from_parsed(
            {"targets": targets}, parsed, confidence_floor=0.45
        )
        self.assertEqual(decision["metadata"]["task"], "medium")
        self.assertTrue(decision["metadata"]["escalated"])
        self.assertEqual(decision["targetIndex"], 1)

    def test_high_risk_escalates(self) -> None:
        parsed = {
            "task": "medium",
            "confidence": 0.9,
            "complexity": 2.0,
            "high_risk": True,
        }
        targets = [
            {"model": "gpt-5.6-luna", "tier": "cheap", "keyConfigured": True},
            {"model": "gpt-5.6-sol", "tier": "medium", "keyConfigured": True},
            {"model": "gpt-6-astra", "tier": "advanced", "keyConfigured": True},
        ]
        decision = self.m.decide_from_parsed(
            {"targets": targets}, parsed, confidence_floor=0.45
        )
        self.assertEqual(decision["targetIndex"], 2)

    def test_model_hint_without_tier(self) -> None:
        targets = [
            {"model": "gpt-5.6-luna", "keyConfigured": True},
            {"model": "gpt-5.6-sol", "keyConfigured": True},
            {"model": "gpt-6-astra", "keyConfigured": True},
        ]
        self.assertEqual(self.m.find_target_index(targets, "simple"), 0)
        self.assertEqual(self.m.find_target_index(targets, "medium"), 1)
        self.assertEqual(self.m.find_target_index(targets, "advanced"), 2)

    def test_noul_field_high_risk(self) -> None:
        parsed = self.m.parse_openjev_answers(
            {
                "answers": {
                    "task": {
                        "choice": "simple",
                        "confidence": 0.99,
                        "probabilities": {"simple": 0.99, "medium": 0.005, "advanced": 0.005},
                    },
                    "complexity": {"score": 0.5},
                    "high_risk": {"type": "noul", "noul": 0.97},
                }
            }
        )
        self.assertTrue(parsed["high_risk"])
        targets = [
            {"model": "gpt-5.6-luna", "tier": "cheap", "keyConfigured": True},
            {"model": "gpt-5.6-sol", "tier": "medium", "keyConfigured": True},
            {"model": "gpt-6-astra", "tier": "advanced", "keyConfigured": True},
        ]
        decision = self.m.decide_from_parsed(
            {"targets": targets}, parsed, confidence_floor=0.45
        )
        self.assertEqual(decision["metadata"]["task"], "medium")
        self.assertTrue(decision["metadata"]["escalated"])

    def test_invalid_task_raises(self) -> None:
        with self.assertRaises(ValueError):
            self.m.parse_openjev_answers({"answers": {"task": {"choice": "nope"}}})


if __name__ == "__main__":
    unittest.main()
