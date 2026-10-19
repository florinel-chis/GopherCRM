"""Atomic journals and a process lock shared by every Langflow worker."""

from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path

from .config import GateError


class Store:
    def __init__(self, root: Path):
        self.root = root

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
            control = {'stopped': True, 'allowed_issue': self.read('active').get('issue')}
            self.write('control', control)
            return control

    def resume_queue(self):
        with self.control_lock():
            self.write('control', {'stopped': False})

    def stopped(self, issue=None):
        control = self.read('control')
        return bool(control.get('stopped') and
                    (issue is None or issue != control.get('allowed_issue')))

    def admit(self, issue: int) -> bool:
        with self.control_lock():
            if self.stopped(issue):
                return False
            active = self.read('active').get('issue')
            if active is not None and active != issue:
                raise GateError(f'Issue #{active} must finish before another ticket can start')
            self.write('active', {'issue': issue})
            return True

    def release(self, issue: int):
        with self.control_lock():
            if self.read('active').get('issue') == issue:
                self.write('active', {})

    @contextmanager
    def exclusive(self):
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(self.root / 'runner.lock', os.O_RDWR | os.O_CREAT, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise GateError('Another runner stage is active for this state directory') from exc
            yield
        finally:
            os.close(fd)
