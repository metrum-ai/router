# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Recreate and verify the preregistered seed355 split before any collection."""
import argparse
import hashlib
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'router-loop'))
import full

SEED_SHA = '5dad6cf400661d747275e7409c358ff1efbab37ca76009b7e8c67476351d3495'
CLASSES_SHA = '679abc785fab6f01cb85dee490500f389e6fe6c19382542be19cc4161befcb81'
MANIFEST_SHA = '45b9b0f0c69f48288608bb9afe37972d0d6141a2801fe7459793f529ac0287fc'
PLAN_SHA = '3310f9cb2d983f72e20cd6a9b3c5489b36758468b30ac00f3b7a0f8411bb1a55'
GOLD_SHA = '00e09198e5572487764a768619d48c6b7083e48a69e98d992501d861b4362483'


def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()


def freeze(pool, out):
    if (sha(pool/'seed-canonical.jsonl') != SEED_SHA or sha(pool/'class-by-normalized-user-sha256.json') != CLASSES_SHA
            or sha(pool/'manifest.json') != MANIFEST_SHA):
        raise ValueError('source pool does not match preregistration')
    manifest = json.loads((pool/'manifest.json').read_text())
    if manifest.get('seed_file_sha256') != SEED_SHA or manifest.get('classes_sha256') != CLASSES_SHA:
        raise ValueError('source manifest does not match preregistration')
    out.mkdir(mode=0o700, parents=False, exist_ok=False)
    full.SEED_SHA = SEED_SHA
    selected = full.prepare(pool, out)
    # The old run-2 helper writes a collection freeze with run-2 budget facts;
    # it is not an authorization or accurate receipt for this run.
    (out/'precollection-freeze.json').unlink()
    if sha(out/'selected-plan.json') != PLAN_SHA or sha(out/'heldout-gold.json') != GOLD_SHA:
        raise ValueError('selected plan differs from preregistration')
    tasks = [row['task_hash'] for group in selected.values() for row in group]
    prompts = [row['source_user_sha256'] for group in selected.values() for row in group]
    if len(tasks) != 3800 or len(set(tasks)) != len(tasks) or len(set(prompts)) != len(prompts):
        raise ValueError('split collision or incorrect cohort size')
    return selected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pool', type=Path, required=True); parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args(); freeze(args.pool, args.out)
    print(json.dumps({'plan_sha256': PLAN_SHA, 'heldout_gold_sha256': GOLD_SHA, 'groups': 3800}))


if __name__ == '__main__': main()
