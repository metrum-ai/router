# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Check the partial SXM4 measurements against their raw samples."""
import collections
import gzip
import json
import math
from pathlib import Path
import statistics
import unittest

import load

ROOT = Path(__file__).resolve().parents[2]
EVIDENCE = ROOT / 'benchmarks/overhead/evidence/a100-sxm-2026-10-31'


class SXMEvidenceTests(unittest.TestCase):
    def test_raw_summaries_and_tables(self):
        readme = (EVIDENCE/'README.md').read_text()
        docs = (ROOT/'docs-site/docs/evaluation/performance-overhead.md').read_text()
        for mode in ('plain','shadow','enforce'):
            directory = EVIDENCE/(mode+'-latency')
            manifest = json.loads((directory/'manifest.json').read_text())
            summary = json.loads((directory/'summary.json').read_text())
            self.assertEqual(manifest['concurrency'],[1,8,32,128])
            self.assertEqual((manifest['repetitions'],manifest['requests_per_worker']),(3,8))
            self.assertEqual(len(summary['pairs']),24)
            rows = collections.defaultdict(list)
            with gzip.open(directory/'samples.jsonl.gz','rt',encoding='utf-8') as f:
                for line in f:
                    row=json.loads(line)
                    self.assertEqual(set(row),{'latency_ms','ttft_content_ms','content_event_gaps_ms','prompt_tokens','completion_tokens','worker','request','route','concurrency','streaming','repetition'})
                    self.assertIn(row['route'],('direct','router'))
                    self.assertIs(type(row['streaming']),bool)
                    for k in ('latency_ms','worker','request','concurrency','repetition'):
                        self.assertTrue(type(row[k]) in (int,float) and math.isfinite(row[k]))
                    self.assertTrue(row['ttft_content_ms'] is None or (type(row['ttft_content_ms']) in (int,float) and math.isfinite(row['ttft_content_ms'])))
                    self.assertTrue(all(type(x) in (int,float) and math.isfinite(x) for x in row['content_event_gaps_ms']))
                    self.assertEqual((row['prompt_tokens'],row['completion_tokens']),(43,32))
                    rows[(row['concurrency'],row['streaming'],row['repetition'],row['route'])].append(row)
            self.assertEqual(sum(map(len,rows.values())),16224)
            for pair in summary['pairs']:
                for route in ('direct','router'):
                    samples=rows[(pair['concurrency'],pair['streaming'],pair['repetition'],route)]
                    self.assertEqual(len(samples),8*pair['concurrency'])
                    key='ttft_content_ms' if pair['streaming'] else 'latency_ms'
                    for p in (50,99):
                        self.assertAlmostEqual(load.percentile([r[key] for r in samples],p/100),pair[route][f'p{p}_ms'])
                        if pair['streaming']:
                            gaps=[x for row in samples for x in row['content_event_gaps_ms']]
                            self.assertAlmostEqual(load.percentile(gaps,p/100),pair[route][f'content_event_gap_p{p}_ms'])
                for p in (50,99):
                    self.assertAlmostEqual(pair['router'][f'p{p}_ms']-pair['direct'][f'p{p}_ms'],pair[f'p{p}_delta_ms'])
            for n in (1,8,32,128):
                means=[]
                for streaming in (False,True):
                    pairs=[p for p in summary['pairs'] if p['concurrency']==n and p['streaming']==streaming]
                    self.assertEqual(len(pairs),3)
                    means += [statistics.mean(p[f'p{q}_delta_ms'] for p in pairs) for q in (50,99)]
                line=f'| {mode} | {n} | '+' | '.join(f'{x:.3f}' for x in means)+' |'
                self.assertIn(line,readme);self.assertIn(line,docs)
                pairs=[p for p in summary['pairs'] if p['concurrency']==n and p['streaming']]
                gaps=[statistics.mean(p['router'][f'content_event_gap_p{q}_ms']-p['direct'][f'content_event_gap_p{q}_ms'] for p in pairs) for q in (50,99)]
                self.assertIn(f'| {mode} | {n} | '+' | '.join(f'{x:.3f}' for x in gaps)+' |',readme)

    def test_profile_scope_and_policy_counts(self):
        status=json.loads((EVIDENCE/'phase-status.json').read_text())
        policy=json.loads((EVIDENCE/'policy-summary.json').read_text())
        for mode in ('shadow','enforce'):
            self.assertEqual(policy[mode+'-latency'],[{'count':9126,'error_class':'','outcome':'shadow_recommended' if mode=='shadow' else 'selected'}])
        self.assertEqual(status['enforce-profile-32']['status'],'failed')
        self.assertFalse((EVIDENCE/'enforce-profile-32').exists())
        for mode,n in (('plain',32),('plain',128),('shadow',32),('shadow',128),('enforce',128)):
            phase=f'{mode}-profile-{n}';self.assertEqual(status[phase]['status'],'passed')
            capture=json.loads((EVIDENCE/phase/'profile-phase.json').read_text())
            self.assertEqual(capture['minimum_active_percent'],90)
            self.assertEqual(capture['stop_guard_min_active'],29 if n==32 else 116)
            self.assertGreaterEqual(capture['poll_min_active_during_observed_window'],capture['stop_guard_min_active'])
            for name in ('cpu.pprof','heap.pprof'):
                self.assertGreater((EVIDENCE/phase/name).stat().st_size,0)


if __name__=='__main__':unittest.main()
