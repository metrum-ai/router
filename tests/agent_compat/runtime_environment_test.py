#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Keep the agent subprocess environment minimal without breaking CI Python."""
import os
import unittest
from unittest.mock import patch

from run_chat_smoke import runtime_environment


class RuntimeEnvironmentTest(unittest.TestCase):
    def test_shared_python_library_path_is_retained_but_credentials_are_not(self):
        with patch.dict(os.environ, {"PATH": "/bin", "HOME": "/tmp", "LD_LIBRARY_PATH": "/python/lib", "UNRELATED_API_KEY": "test-only-value"}, clear=True):
            env = runtime_environment()
        self.assertEqual(env["LD_LIBRARY_PATH"], "/python/lib")
        self.assertNotIn("UNRELATED_API_KEY", env)
        self.assertEqual(env["GOPROXY"], "off")

    def test_library_path_is_optional(self):
        with patch.dict(os.environ, {"PATH": "/bin", "HOME": "/tmp"}, clear=True):
            env = runtime_environment()
        self.assertNotIn("LD_LIBRARY_PATH", env)


if __name__ == "__main__":
    unittest.main()
