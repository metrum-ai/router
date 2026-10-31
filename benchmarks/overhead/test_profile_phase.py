# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic checks for strict and explicitly relaxed profile occupancy."""
import json
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

import profile_phase as profile


def arguments(root, concurrency):
    prompt = root / "prompt.txt"
    prompt.write_text("synthetic")
    return [
        "profile_phase.py", "--router-url", "http://127.0.0.1:1234/v1/chat/completions",
        "--control-socket", str(root / "sock"), "--model", "chat", "--path-kind", "plain",
        "--prompt-file", str(prompt), "--output-tokens", "32", "--concurrency", str(concurrency),
        "--minimum-sampling-seconds", "0.01", "--deadline-seconds", "3",
        "--output-dir", str(root / "capture"),
    ]


class ProfileOccupancyTests(unittest.TestCase):
    def test_strict_default_and_opt_in_90_percent(self):
        cases = ((32, 100, 32), (32, 90, 29), (128, 100, 128), (128, 90, 116))
        for concurrency, percent, floor in cases:
            with self.subTest(concurrency=concurrency, percent=percent), tempfile.TemporaryDirectory() as d:
                root = Path(d)
                done = threading.Event()
                commands = []
                args = arguments(root, concurrency)
                if percent == 90:
                    args.extend(["--minimum-active-percent", "90"])

                def phase(*args):
                    args[-1]()
                    if not done.wait(2):
                        raise RuntimeError("control did not stop")
                    return [{"prompt_tokens": 3}], None

                def control(_path, command):
                    commands.append(command)
                    if command in ("start", "status"):
                        return {"ok": True, "active": floor}
                    if command == f"stop{floor}":
                        done.set()
                        return {"ok": True, "active": floor}
                    raise AssertionError(command)

                with patch.object(profile.load, "run_phase", side_effect=phase), \
                     patch.object(profile, "control", side_effect=control), \
                     patch.object(sys, "argv", args):
                    profile.main()
                result = json.loads((root / "capture/profile-phase.json").read_text())
                self.assertEqual(result["concurrency"], concurrency)
                self.assertEqual(result["minimum_active_percent"], percent)
                self.assertEqual(result["guard_min_active_during_capture"], floor)
                self.assertIn(f"stop{floor}", commands)

    def test_stop_diagnostic_is_allowlisted(self):
        cases = (("invalid profile: active chat requests fell to 31 during capture", "fell to 31"),
                 ("arbitrary socket data MUST_NOT_LOG", "profile capture invalid"))
        for error, expected in cases:
            with self.subTest(error=error), tempfile.TemporaryDirectory() as d:
                root = Path(d)
                done = threading.Event()
                args = arguments(root, 32)

                def phase(*args):
                    args[-1]()
                    done.wait(2)
                    return [{"prompt_tokens": 3}], None

                def control(_path, command):
                    if command in ("start", "status"):
                        return {"ok": True, "active": 32}
                    done.set()
                    return {"ok": False, "active": 32, "error": error}

                with patch.object(profile.load, "run_phase", side_effect=phase), \
                     patch.object(profile, "control", side_effect=control), \
                     patch.object(sys, "argv", args):
                    with self.assertRaises(SystemExit) as failure:
                        profile.main()
                self.assertIn(expected, str(failure.exception))
                self.assertNotIn("MUST_NOT_LOG", str(failure.exception))
                self.assertFalse((root / "capture/profile-phase.json").exists())

    def test_workload_error_not_masked_by_early_finish(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            args = arguments(root, 32)
            with patch.object(profile.load, "run_phase", side_effect=profile.load.ProbeError("HTTP status 503")), \
                 patch.object(sys, "argv", args):
                with self.assertRaises(SystemExit) as failure:
                    profile.main()
            self.assertIn("HTTP status 503", str(failure.exception))
            self.assertFalse((root / "capture/profile-phase.json").exists())


if __name__ == "__main__":
    unittest.main()
