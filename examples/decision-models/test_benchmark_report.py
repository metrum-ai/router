# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('benchmark_report',HERE/'benchmark_report.py')
report=importlib.util.module_from_spec(spec);spec.loader.exec_module(report)


class BenchmarkReportTests(unittest.TestCase):
    def test_dataset_is_labeled_and_unique(self):
        rows=report.dataset(HERE/'benchmark_dataset.json')
        self.assertEqual(len(rows),12)
        self.assertEqual({r['domain'] for r in rows},{'math','code','writing'})
        self.assertEqual({r['expected_tier'] for r in rows},{'cheap','strong'})
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)/'bad.json';path.write_text(json.dumps({'version':1,'provenance':'x','rows':[rows[0],rows[0]]}))
            with self.assertRaises(ValueError):report.dataset(path)

    def test_confusion_counts_and_errors_are_not_discarded(self):
        base={'decision':'d3','mode':'shadow'}
        rows=[dict(base,expected_tier='cheap',recommended_tier='cheap',outcome='shadow_recommended',policy_duration_ms=5),
              dict(base,expected_tier='strong',recommended_tier='cheap',outcome='shadow_recommended',policy_duration_ms=10),
              dict(base,expected_tier='strong',recommended_tier=None,outcome='error',policy_duration_ms=15)]
        result=report.summarize(rows)[0]
        self.assertEqual((result['count'],result['successes'],result['errors'],result['correct']),(3,2,1,1))
        self.assertEqual(result['accuracy_successes_only'],.5)
        self.assertAlmostEqual(result['correct_fraction_all_requests'],1/3)
        self.assertEqual(result['confusion']['strong']['cheap'],1)
        self.assertEqual(result['policy_p50_ms'],5)
        self.assertIsNone(result['policy_p99_ms'])

    def test_paid_backend_rejected_before_credential_or_network_use(self):
        result=subprocess.run([sys.executable,str(HERE/'run.py'),'--router','/nonexistent',
                               '--backend','fireworks','--benchmark-dataset',str(HERE/'benchmark_dataset.json')],capture_output=True,text=True,timeout=10)
        self.assertNotEqual(result.returncode,0)
        self.assertIn('does not support paid hosted generation',result.stderr)

    def test_wrong_mode_outcome_is_an_error(self):
        row={'decision':'d3','mode':'enforce','expected_tier':'cheap','recommended_tier':'cheap','outcome':'shadow_recommended'}
        summary=report.summarize([row])[0]
        self.assertEqual((summary['successes'],summary['errors']),(0,1))

    def test_p99_requires_100_successful_timings(self):
        rows=[{'decision':'clef','mode':'enforce','expected_tier':'cheap','recommended_tier':'cheap','outcome':'selected','policy_duration_ms':n} for n in range(100)]
        self.assertEqual(report.summarize(rows)[0]['policy_p99_ms'],98)


if __name__=='__main__':unittest.main()
