# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check_xcrypto_openpgp.py")


class CheckXcryptoOpenPGPTest(unittest.TestCase):
    def run_check(self, packages: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT)], input=packages, text=True, capture_output=True, check=False
        )

    def test_accepts_other_crypto_packages(self) -> None:
        result = self.run_check(
            "golang.org/x/crypto/acme\ngolang.org/x/crypto/bcrypt\n"
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_openpgp_and_subpackages(self) -> None:
        for package in (
            "golang.org/x/crypto/openpgp",
            "golang.org/x/crypto/openpgp/armor",
        ):
            with self.subTest(package=package):
                result = self.run_check(package + "\n")
                self.assertEqual(result.returncode, 1)
                self.assertIn(package, result.stderr)


if __name__ == "__main__":
    unittest.main()
