# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Dependency-free integration fixture for an external trainer and frozen gate.

This produces metadata for a pre-registered synthetic candidate target. It does
not train model weights or establish model quality.
"""
import argparse
import hashlib
import json
from pathlib import Path
import sys
import zipfile


def run(export_zip: Path, gate_path: Path, output: Path, candidate_target: str) -> bool:
    gate = json.loads(gate_path.read_text())
    with zipfile.ZipFile(export_zip) as archive:
        manifest = json.loads(archive.read('manifest.json'))
        rows = [json.loads(line) for line in archive.read('train.jsonl').splitlines() if line]
        expected = manifest.get('files', {}).get('train.jsonl', {}).get('sha256')
        actual = hashlib.sha256(archive.read('train.jsonl')).hexdigest()
        if not expected or expected != actual:
            raise ValueError('training split checksum does not match export manifest')
    successes = sum(bool(row.get('outcome', {}).get('task_success')) for row in rows)
    rate = successes / len(rows) if rows else 0.0
    passed = (len(rows) >= int(gate['minimum_train_rows']) and
              rate >= float(gate['minimum_train_success_rate']))
    result = {
        'format': 'router-external-trainer-fixture-v1',
        'synthetic_standin': True,
        'dataset_hash': manifest.get('dataset_hash', ''),
        'train_rows': len(rows),
        'train_success_rate': rate,
        'candidate_target': candidate_target,
        'candidate_artifact_sha256': hashlib.sha256(
            (manifest.get('dataset_hash', '') + candidate_target).encode()).hexdigest(),
        'gate': {'id': gate['id'], 'passed': passed},
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(result, sort_keys=True) + '\n')
    return passed


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--export-zip', type=Path, required=True)
    parser.add_argument('--preregistered-gate', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--candidate-target', default='mock/m-two')
    args = parser.parse_args()
    try:
        return 0 if run(args.export_zip, args.preregistered_gate, args.out, args.candidate_target) else 2
    except (KeyError, OSError, ValueError, zipfile.BadZipFile, json.JSONDecodeError) as error:
        print(f'external training fixture failed: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
