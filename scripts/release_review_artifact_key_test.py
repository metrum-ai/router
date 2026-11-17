# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import unittest

from release_review_artifact_key import artifact_key


class ReleaseReviewArtifactKeyTest(unittest.TestCase):
    def test_same_range_has_same_key_and_changed_range_has_new_key(self) -> None:
        first = artifact_key("v4.8.0", "c" * 40, "a" * 40)
        self.assertEqual(first, artifact_key("v4.8.0", "c" * 40, "a" * 40))
        self.assertNotEqual(first, artifact_key("v4.8.0", "c" * 40, "b" * 40))
        self.assertNotEqual(first, artifact_key("v4.9.0", "c" * 40, "a" * 40))
        self.assertNotEqual(first, artifact_key("v4.8.0", "d" * 40, "a" * 40))
        self.assertRegex(first, r"^[0-9a-f]{24}$")

    def test_rejects_empty_and_multiline_ranges(self) -> None:
        for base, head in [("", "abc"), ("v4.8.0", "a\nHEAD")]:
            with self.subTest(base=base, head=head), self.assertRaises(ValueError):
                artifact_key(base, "c" * 40, head)


if __name__ == "__main__":
    unittest.main()
