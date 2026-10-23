#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""High admission limits for isolated localhost evaluation, NOT production defaults.

Apply after EVERY caller creation and before inference. Deliberate rate/quota
negative tests must be labelled and use a disposable profile. External resource
budgets/deadlines remain mandatory; these are not spending controls.
"""
import argparse
from pathlib import Path
import yaml

RATE = {'rpm': 10000, 'tpm': 1000000000, 'concurrent': 128}
QUOTA = {'day': {'requests': 1000000, 'tokens': 1000000000},
         'month': {'requests': 1000000, 'tokens': 1000000000}}


def configure(config):
    for caller in config.get('callers', []):
        caller['rate'] = dict(RATE)
        caller['quota'] = {k: dict(v) for k, v in QUOTA.items()}
        caller['traffic_shape'] = {'enabled': False}
    config['server']['traffic_shape'] = {'enabled': False}
    # Project records are membership/status only: no project rate/quota fields.
    # Script groups have a separate admission cap; other group strategies do not.
    for group in config.get('models', {}).values():
        if group.get('strategy') == 'script':
            group['script_max_concurrent'] = 128
    return config


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    class Loader(yaml.SafeLoader):
        pass
    Loader.yaml_implicit_resolvers = {k: [(t, r) for t, r in v if t != 'tag:yaml.org,2002:timestamp']
                                     for k, v in Loader.yaml_implicit_resolvers.items()}
    config = yaml.load(args.config.read_text(), Loader=Loader)
    if not config['server']['listen'].startswith(('127.0.0.1:', '[::1]:')):
        parser.error('isolated literal localhost listener required; never raise production limits')
    args.config.write_text(yaml.safe_dump(configure(config)))
    args.config.chmod(0o600)
    print('All localhost evaluation callers high-limit; script groups128; project records have no rate fields. NOT a production/budget policy.')


if __name__ == '__main__':
    main()
