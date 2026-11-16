# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import unittest

from select_previous_release import select_previous


class SelectPreviousReleaseTest(unittest.TestCase):
    def test_selects_latest_production_release_and_skips_rc_and_draft(self) -> None:
        releases = [
            {"tagName": "v4.8.0", "isPrerelease": False, "isDraft": False, "publishedAt": "2026-01-01T00:00:00Z"},
            {"tagName": "v5.0.0-rc.1", "isPrerelease": True, "isDraft": False, "publishedAt": "2026-10-01T00:00:00Z"},
            {"tagName": "v4.9.0", "isPrerelease": False, "isDraft": False, "publishedAt": "2026-09-01T00:00:00Z"},
            {"tagName": "v5.0.0", "isPrerelease": False, "isDraft": True, "publishedAt": "2026-11-01T00:00:00Z"},
        ]
        self.assertEqual(select_previous(releases, "v5.0.0"), "v4.9.0")

    def test_current_tag_is_excluded(self) -> None:
        releases = [{"tagName": "v5.0.0", "isPrerelease": False, "isDraft": False, "publishedAt": "2026-11-20T00:00:00Z"}]
        with self.assertRaisesRegex(ValueError, "no previous"):
            select_previous(releases, "v5.0.0")


if __name__ == "__main__":
    unittest.main()
