"""Atomic journals and a process lock shared by every Langflow worker."""

from contextlib import contextmanager
import fcntl
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
