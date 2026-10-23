#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Print installed distribution versions without requiring pip in the image."""
from importlib.metadata import distributions

if __name__ == '__main__':
    print('# Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0')
    for name, version in sorted((d.metadata['Name'], d.version) for d in distributions()):
        print(f'{name}=={version}')
