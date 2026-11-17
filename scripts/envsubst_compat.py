#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Minimal envsubst-compatible stdin expander for release tooling."""

from __future__ import annotations

import os
import sys


sys.stdout.write(os.path.expandvars(sys.stdin.read()))
