"""Explicitly reclaim only rebuildable dependencies of completed validation trees."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

from .config import GateError
from .operations import journals
from .resources import heavy_lease
from .state import Store


def regular_path(path):
    return not any(p.is_symlink() for p in [path, *path.parents])


def git(tree, *args):
    result = subprocess.run(['git', *args], cwd=tree, capture_output=True, text=True, timeout=15)
    if result.returncode:
        raise GateError('Cannot verify retained checkout: ' + str(tree))
    return result.stdout.strip()


def inventory(target):
    count, size = 0, 0
    for directory, dirs, files in os.walk(target, followlinks=False):
        for name in files + [d for d in dirs if (Path(directory) / d).is_symlink()]:
            stat = (Path(directory) / name).lstat()
            count += 1
            size += stat.st_size
    return count, size


def preview(config):
    root = config.state_dir
    candidates = []
    if not regular_path(root):
        raise GateError('Retention refuses a state path through a symlink')
    records = list(journals(root))
    # An active marker or any nonterminal reference protects that entire ticket,
    # including older attempts needed for interrupted validation recovery.
    active = Store(root).read('active')
    protected = {active.get('issue'), active.get('pr')}
    for _, record in records:
        if record.get('stage') != 'merged' or not record.get('merge_commit'):
            protected.update([record.get('issue'), record.get('pr')])
    for journal, record in records:
        number = record.get('issue') if journal.name.startswith('issue-') else record.get('pr')
        if type(number) is not int or number in protected or not record.get('merge_commit'):
            continue
        heads = {record.get('head')} | {a.get('head') for a in record.get('attempts', [])}
        for tree in sorted((root / 'validation').glob(f'{number}-*')):
            if not re.fullmatch(rf'{number}-[0-9a-f]{{12}}', tree.name) or not regular_path(tree):
                continue
            target = tree / 'gocrm-ui/node_modules'
            if not target.is_dir() or not regular_path(target):
                continue
            head = git(tree, 'rev-parse', 'HEAD')
            if head not in heads or git(tree, 'status', '--porcelain', '--untracked-files=all'):
                continue
            if git(tree, 'ls-files', '--', 'gocrm-ui/node_modules'):
                raise GateError('Refusing tracked dependency files')
            if not git(tree, 'check-ignore', 'gocrm-ui/node_modules'):
                raise GateError('Dependencies must be ignored by the validation checkout')
            count, size = inventory(target)
            stat = target.stat()
            candidates.append({'path': str(target), 'head': head, 'entries': count, 'bytes': size,
                               'device': stat.st_dev, 'inode': stat.st_ino})
    body = {'candidates': candidates, 'reclaimable_bytes': sum(c['bytes'] for c in candidates)}
    body['plan_hash'] = hashlib.sha256(json.dumps(body, sort_keys=True).encode()).hexdigest()
    return body


def reclaim(config, *, apply=False, plan_hash=None):
    store = Store(config.state_dir)
    with store.exclusive(), heavy_lease():
        selected = preview(config)
        if not apply:
            return dict(selected, applied=False)
        if plan_hash != selected['plan_hash']:
            raise GateError('Retention selection changed or no preview hash supplied; preview again')
        if not shutil.rmtree.avoids_symlink_attacks:
            raise GateError('This platform cannot safely remove dependency trees')
        removed = []
        for candidate in selected['candidates']:
            target = Path(candidate['path'])
            stat = target.stat()
            if not regular_path(target) or (stat.st_dev, stat.st_ino) != (candidate['device'], candidate['inode']):
                raise GateError('Dependency identity changed during retention')
            shutil.rmtree(target)
            removed.append(str(target))
        result = dict(selected, applied=True, removed=removed)
        store.write('last-retention', result)
        return result
