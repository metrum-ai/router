# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Require advertised storage and a real mounted training filesystem."""
import json
import math
from pathlib import Path
import shutil

MIN_ADVERTISED_GB=300
MIN_TOTAL_BYTES=300_000_000_000
MIN_FREE_BYTES=120_000_000_000


def offer_storage_ok(offer):
    """Both supplied API storage fields must satisfy the minimum."""
    try:
        top=float(offer.get('storage_in_gb') or 0)
        nested=offer.get('configuration',{}).get('storage_in_gb')
        return math.isfinite(top) and top>=MIN_ADVERTISED_GB and (nested is None or math.isfinite(float(nested)) and float(nested)>=MIN_ADVERTISED_GB)
    except (TypeError,ValueError,AttributeError):return False


def select(candidates,usage=shutil.disk_usage):
    """Choose an existing, non-symlink filesystem, never format or mount disks."""
    viable=[]
    for path in candidates:
        p=Path(path)
        if not p.is_absolute() or p.is_symlink() or not p.is_dir():continue
        try:stats=usage(p)
        except OSError:continue
        if stats.total>=MIN_TOTAL_BYTES and stats.free>=MIN_FREE_BYTES:viable.append((stats.free,str(p),stats.total))
    if not viable:raise RuntimeError('no mounted filesystem meets 300GB total and 120GB free')
    free,base,total=max(viable)
    return {'base':base,'total_bytes':total,'free_bytes':free}


def main():
    home=Path.home();candidates=[home,Path('/ephemeral'),Path('/mnt'),Path('/mnt/data'),Path('/workspace'),Path('/data')]
    for root in (Path('/mnt'),Path('/ephemeral')):
        if root.is_dir() and not root.is_symlink():
            try:candidates.extend(x for x in root.iterdir() if x.is_dir() and not x.is_symlink())
            except OSError:pass
    print(json.dumps(select(candidates),sort_keys=True))

if __name__=='__main__':main()
