# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Freeze deterministic public benchmark subsets; never run model-generated code."""
import argparse
import csv
import hashlib
import json
from pathlib import Path
import re

SOURCES = {
    'medical.parquet': 'b768a1ea34afc9f80d3106d9b21f80fa8a00ec450a1f6cd641af72ca9e591021',
    'financial.csv': '31ea8274c98b52212daa68f2fb962811bf1913c1efe51b99765c8e2311806de6',
    'math.parquet': 'ee7b8da9e381df27b9e3f7758a159ab2bdaa4dbaa910546cbbc47e0cb44e4f59',
    'coding.parquet': 'ceee643a6663effa6aff3758bec0a2c258e5896c926df70de713a34900a81c70',
}
COUNT = 20
SEED = 'expert-routing-v1-426'


def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def csv_rows(path):
    with path.open(encoding='utf-8', newline='') as file: return list(csv.DictReader(file))


def pick(domain, rows):
    # Rank raw public rows before seeing any model outputs or classifier scores.
    ordered = sorted(rows, key=lambda row: hashlib.sha256((SEED+'\0'+domain+'\0'+json.dumps(row, sort_keys=True, ensure_ascii=False)).encode()).digest())
    if len(ordered) < COUNT: raise ValueError('not enough eligible rows for '+domain)
    return ordered[:COUNT]


def prepare(root):
    import pyarrow.parquet as parquet
    for name, expected in SOURCES.items():
        if sha(root/name) != expected: raise ValueError('source hash mismatch: '+name)
    result = []
    bio = []
    for row in parquet.read_table(root/'medical.parquet').to_pylist():
        choices = [row.get(k) for k in ('opa','opb','opc','opd')]
        answer = row.get('cop'); question = row.get('question')
        if isinstance(question, str) and len(question) <= 7000 and all(isinstance(v,str) for v in choices) and isinstance(answer, int) and 0 <= answer < 4:
            prompt = 'Choose A, B, C, or D. Answer with only the letter.\n'+question+'\n'+'\n'.join(f'{letter}. {choice}' for letter,choice in zip('ABCD',choices))
            bio.append({'prompt':prompt, 'expected':'ABCD'[answer], 'source_id':row['id']})
    result += [{'domain':'biomedical', **r} for r in pick('biomedical', bio)]
    finance = []
    for row in csv_rows(root/'financial.csv'):
        text = row['text'].strip()
        if row['label'] in ('0','1','2') and text and len(text) <= 2000 and not re.search(r'https?://|@[\w]+|[\w.+-]+@[\w.-]+', text):
            finance.append({'prompt':'Classify the following as Bearish, Bullish, or Neutral. Answer with only the label.\n'+text,
                            'expected':{'0':'Bearish','1':'Bullish','2':'Neutral'}[row['label']]})
    result += [{'domain':'finance', **r} for r in pick('finance', finance)]
    math = []
    for row in parquet.read_table(root/'math.parquet').to_pylist():
        question = row.get('question'); answer = row.get('answer')
        if not isinstance(question,str) or not isinstance(answer,str) or len(question) > 7000: continue
        match = re.search(r'####\s*([+-]?[\d,]+(?:\.\d+)?)\s*$', answer)
        if match:
            math.append({'prompt':'Solve the following. Return only the final numeric answer.\n'+question,
                         'expected':match.group(1).replace(',','')})
    result += [{'domain':'math', **r} for r in pick('math', math)]
    coding = []
    for row in parquet.read_table(root/'coding.parquet').to_pylist():
        choices = row['choices']; answer = row['answer']
        if isinstance(choices, list) and len(choices) == 4 and isinstance(answer, str) and len(answer) == 1 and answer in 'ABCD' and isinstance(row['question'], str) and len(row['question']) <= 7000:
            prompt = 'Choose A, B, C, or D. Answer with only the letter.\n'+row['question']+'\n'+'\n'.join(f'{letter}. {choice}' for letter,choice in zip('ABCD',choices))
            coding.append({'prompt':prompt, 'expected':answer, 'source_id':row['task_id']})
    result += [{'domain':'coding', **r} for r in pick('coding', coding)]
    # Fixed synthetic controls test the routing fallback, not domain quality.
    controls = [('Repeat only the word violet.','violet'), ('Repeat only the word river.','river'),
                ('Repeat only the word mountain.','mountain'), ('Repeat only the word cloud.','cloud'),
                ('Repeat only the word sunset.','sunset'), ('Repeat only the word autumn.','autumn'),
                ('Repeat only the word lantern.','lantern'), ('Repeat only the word garden.','garden')]
    result += [{'domain':'out-of-domain','prompt':prompt,'expected':answer} for prompt,answer in controls]
    for i, item in enumerate(result):
        item['id'] = f'{item["domain"]}-{i % COUNT:02d}'
        item.setdefault('tools', [])
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__); parser.add_argument('--data-dir', required=True, type=Path); parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args(); suite = prepare(args.data_dir)
    payload = json.dumps(suite, sort_keys=True, ensure_ascii=False, indent=2)+'\n'
    with args.out.open('x', encoding='utf-8') as file: file.write(payload)
    print(json.dumps({'cases':len(suite), 'sha256':sha(args.out)}))


if __name__ == '__main__': main()
