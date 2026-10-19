"""Bounded subprocess execution with cancellation and private diagnostic logs."""

import asyncio
from datetime import datetime, timezone
import os
from pathlib import Path
import re
import signal
import uuid

from .config import CommandError, GateError


def redact(text: str) -> str:
    for name, value in os.environ.items():
        if len(value) >= 8 and re.search(r'TOKEN|SECRET|PASSWORD|API_KEY|DSN', name, re.I):
            text = text.replace(value, '[REDACTED]')
    return text


class Commands:
    def __init__(self, log_dir: Path):
        self.log_dir = log_dir

    async def run(self, args: list[str] | tuple[str, ...], *, cwd: Path,
                  timeout: int = 120, input_text: str | None = None,
                  check: bool = True) -> tuple[int, str]:
        self.log_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S')
        path = self.log_dir / f'{stamp}-{uuid.uuid4().hex[:8]}.log'
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        # Stream to disk, rather than retaining unlimited agent/test output in RAM.
        with os.fdopen(fd, 'wb') as output:
            proc = await asyncio.create_subprocess_exec(
                *args, cwd=cwd, stdin=asyncio.subprocess.PIPE,
                stdout=output, stderr=asyncio.subprocess.STDOUT, start_new_session=True,
            )
            try:
                await asyncio.wait_for(
                    proc.communicate(input_text.encode() if input_text is not None else None), timeout,
                )
            except BaseException:
                try:
                    os.killpg(proc.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    await asyncio.wait_for(proc.wait(), 5)
                except asyncio.TimeoutError:
                    try:
                        os.killpg(proc.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    await proc.wait()
                raise
        with path.open('rb') as stream:
            raw = stream.read(20 * 1024 * 1024 + 1)
        if len(raw) > 20 * 1024 * 1024:
            raise GateError(f'Command output exceeds 20 MiB; inspect private log {path}')
        text = raw.decode(errors='replace')
        path.write_text(redact(text))
        if check and proc.returncode:
            raise CommandError(f'{args[0]} exited {proc.returncode}; log {path}\n{redact(text[-12000:])}')
        return proc.returncode, text
