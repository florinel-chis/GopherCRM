"""Atomic journals and a process lock shared by every Langflow worker."""

from contextlib import contextmanager
import asyncio
import fcntl
import hashlib
import json
import os
import re
import threading
import uuid
from pathlib import Path

from .config import GateError


class Store:
    def __init__(self, root: Path):
        self.root = root
        self.batch_id = None
        self._lock_owner = None

    def read(self, name: str) -> dict:
        path = self.root / f'{name}.json'
        return json.loads(path.read_text()) if path.exists() else {}

    def write(self, name: str, data: dict) -> None:
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        target = self.root / f'{name}.json'
        temp = target.with_suffix('.tmp')
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, 'w') as out:
            json.dump(data, out, indent=2)
            out.flush()
            os.fsync(out.fileno())
        os.replace(temp, target)
        directory = os.open(self.root, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def transaction(self, updates):
        """Persist an authorized multi-journal update before applying any member.

        Caller holds exclusive(). Recovery only accepts exact before/after images,
        so interrupted writes cannot overwrite later or externally changed state.
        """
        if self._lock_owner is None or self.read('pending-update'):
            raise GateError('Journal transaction requires the lock and no pending update')
        intent = {'id': uuid.uuid4().hex, 'batch_id': self.batch_id, 'updates': [
            {'name': name, 'before': self.read(name), 'after': data} for name, data in updates.items()]}
        self.write('pending-update', intent)
        self.recover_update()

    def recover_update(self):
        intent = self.read('pending-update')
        if not intent:
            return
        if self._lock_owner is None or intent.get('batch_id') != self.batch_id:
            raise GateError('Pending journal update belongs to another execution owner')
        changes = intent.get('updates', [])
        if (not isinstance(changes, list) or len(changes) != 2 or not self.batch_id
                or any(not isinstance(c, dict) or set(c) != {'name', 'before', 'after'}
                       or not isinstance(c['name'], str)
                       or not re.fullmatch(r'(?:batch-[a-z][a-z0-9-]{0,63}|issue-[0-9]+|completion-pr-[0-9]+)', c['name'])
                       or not isinstance(c['before'], dict) or not isinstance(c['after'], dict) for c in changes)
                or len({c['name'] for c in changes}) != 2
                or f'batch-{self.batch_id}' not in {c['name'] for c in changes}):
            raise GateError('Invalid pending journal update')
        for change in changes:
            if self.read(change['name']) not in (change['before'], change['after']):
                raise GateError('Journal changed during interrupted update; inspect retained intent')
        for change in changes:
            self.write(change['name'], change['after'])
        self.write('pending-update', {})

    def journal(self, issue: int) -> dict:
        """Read a versioned journal, retaining exact legacy bytes before conversion.

        Conversion only permits read-only recovery. It does not adopt unfinished
        work into the new executable policy or invent acceptance evidence.
        Call under the runner lock when execution can update this record.
        """
        name = f'issue-{issue}'
        record = self.read(name)
        if not record:
            return record
        version = record.get('schema_version', 1)
        if type(version) is not int or version not in {1, 2}:
            raise GateError('Unknown/newer journal schema; use the matching runner')
        if version == 1:
            raw = (self.root / f'{name}.json').read_bytes()
            digest = hashlib.sha256(raw).hexdigest()
            backup = self.root / f'{name}.legacy-{digest}.json'
            try:
                fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            except FileExistsError:
                if backup.read_bytes() != raw:
                    raise GateError('Legacy journal backup is inconsistent')
            else:
                with os.fdopen(fd, 'wb') as out:
                    out.write(raw)
                    out.flush()
                    os.fsync(out.fileno())
            record.update(schema_version=2, legacy=True, legacy_backup=str(backup),
                          completion_target='merge')
            self.write(name, record)
        return record

    @contextmanager
    def control_lock(self):
        """Short lock, separate from the long-running execution lock."""
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(self.root / 'control.lock', os.O_RDWR | os.O_CREAT, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX)
            yield
        finally:
            os.close(fd)

    def stop_after_current(self):
        with self.control_lock():
            control = {'stopped': True, 'allowed_issue': self.read('active').get('issue'),
                       'stop_token': uuid.uuid4().hex}
            self.write('control', control)
            return control

    def resume_queue(self):
        with self.control_lock():
            self.write('control', dict(self.read('control'), stopped=False, allowed_issue=None))

    def stopped(self, issue=None):
        control = self.read('control')
        return bool(control.get('stopped') and
                    (issue is None or issue != control.get('allowed_issue')))

    def admit(self, issue: int) -> bool:
        with self.control_lock():
            if self.stopped(issue):
                return False
            if self.read('active').get('pr') is not None:
                raise GateError('An existing PR completion must finish before another ticket can start')
            active = self.read('active').get('issue')
            if active is not None and active != issue:
                raise GateError(f'Issue #{active} must finish before another ticket can start')
            self.write('active', {'issue': issue})
            return True

    def admit_pr(self, number: int):
        """An explicit PR completion does not resume or drain the issue queue."""
        with self.control_lock():
            active = self.read('active')
            if active and active != {'pr': number}:
                raise GateError('Another ticket or PR completion must finish first')
            self.write('active', {'pr': number})

    def release_pr(self, number: int):
        with self.control_lock():
            if self.read('active') == {'pr': number}:
                self.write('active', {})

    def release(self, issue: int):
        with self.control_lock():
            if self.read('active').get('issue') == issue:
                self.write('active', {})

    @contextmanager
    def exclusive(self):
        # A supervisor calls the existing ticket/PR APIs under this same lock.
        # Only the same task on the same Store may nest it; another task or
        # process must still acquire flock, even if it supplies the batch ID.
        try:
            task = asyncio.current_task()
        except RuntimeError:
            task = None
        identity = (threading.get_ident(), task)
        if self._lock_owner == identity:
            yield
            return
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(self.root / 'runner.lock', os.O_RDWR | os.O_CREAT, 0o600)
        acquired = False
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise GateError('Another runner stage is active for this state directory') from exc
            owner = self.read('batch-owner')
            if owner and owner.get('id') != self.batch_id:
                raise GateError('A batch owns this queue; resume or abandon that batch first')
            self._lock_owner = identity
            acquired = True
            self.recover_update()
            yield
        finally:
            if acquired:
                self._lock_owner = None
            os.close(fd)
