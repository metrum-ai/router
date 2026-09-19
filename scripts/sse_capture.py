#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Placeholder for live SSE fixture capture (make sse-capture).

Will record raw upstream SSE under testdata/sse/<provider>/<dialect>/<shape>.sse
from live providers using env.json, stripping Authorization, x-api-key, and
*-request-id headers. Not implemented yet.
"""

from __future__ import annotations

import sys


def main() -> int:
    print(
        "sse_capture: not yet implemented "
        "(scaffold only; live capture lands with the stream fixture harness)",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
