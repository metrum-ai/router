#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic usage reconciliation tests. No provider traffic."""
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest


class ExtractTests(unittest.TestCase):
    def test_join_and_missing_request_fail_closed(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); db = sqlite3.connect(root/'usage.db')
            db.execute('create table request_usage (request_id text, requested_model text, status int, attempts int, fallback_used int, input_tokens int, cached_input_tokens int, output_tokens int, total_cost_usd real, target_model text, pricing_source text, pricing_updated_at text, input_price_per_million_usd real, cached_input_price_per_million_usd real, output_price_per_million_usd real, cost_list_usd real, cost_billed_usd real, usage_source text)')
            rows = []
            for arm in ('frontier', 'routed'):
                for trial in ('leave', 'refund', 'security', 'shipping', 'invoice'):
                    rid = arm+'-'+trial
                    rows.append({'arm':arm, 'trial_id':trial, 'passed':True, 'error':None, 'request_id':rid, 'status':200, 'latency_ms':10})
                    db.execute('insert into request_usage values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)', (rid, arm, 200, 1, 0, 10, 0, 2, 0.00003, 'gpt-6.1-sol', 'https://example.org/price', '2026-01-01', 1, 0, 10, 0.00003, 0.00003, 'reported'))
            db.commit(); db.close()
            (root/'results').write_text(''.join(json.dumps(row)+'\n' for row in rows))
            script = Path(__file__).with_name('extract.py')
            cmd = [sys.executable, str(script), '--workload', 'document-qa', '--results', str(root/'results'), '--database', str(root/'usage.db'), '--out-dir', str(root/'out')]
            p = subprocess.run(cmd, capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stderr)
            summary = json.loads((root/'out'/'summary.json').read_text())
            self.assertEqual(summary['arms']['frontier']['router_recorded_cost_usd'], '0.00015')
            self.assertIsNone(summary['arms']['frontier']['provider_billed_usd'])
            self.assertNotIn('frontier-leave', (root/'out'/'usage.json').read_text())
            rows[0]['request_id'] = 'missing'
            (root/'results').write_text(''.join(json.dumps(row)+'\n' for row in rows))
            cmd[-1] = str(root/'failed')
            self.assertNotEqual(subprocess.run(cmd, capture_output=True).returncode, 0)
            self.assertFalse((root/'failed').exists())
            rows[0]['request_id'] = rows[1]['request_id']
            (root/'results').write_text(''.join(json.dumps(row)+'\n' for row in rows))
            self.assertNotEqual(subprocess.run(cmd, capture_output=True).returncode, 0)
            self.assertFalse((root/'failed').exists())


if __name__ == '__main__': unittest.main()
