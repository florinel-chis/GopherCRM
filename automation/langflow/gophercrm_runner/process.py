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
                  check: bool = True, env: dict[str, str] | None = None, on_start=None) -> tuple[int, str]:
        self.log_dir.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.log_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S')
        path = self.log_dir / f'{stamp}-{uuid.uuid4().hex[:8]}.log'
        error_path = path.with_suffix('.stderr.log')
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        error_fd = os.open(error_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        # Stream to disk, rather than retaining unlimited agent/test output in RAM.
        try:
            with os.fdopen(fd, 'wb') as output, os.fdopen(error_fd, 'wb') as errors:
                proc = await asyncio.create_subprocess_exec(
                    *args, cwd=cwd, stdin=asyncio.subprocess.PIPE,
                    stdout=output, stderr=errors, start_new_session=True, env=env,
                )
                try:
                    if on_start is not None:
                        on_start(proc.pid)
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
                        pass
                    # The parent may exit before children that ignore SIGTERM.
                    try:
                        os.killpg(proc.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    await proc.wait()
                    raise
        finally:
            outputs = []
            truncated = False
            for log in (path, error_path):
                with log.open('rb') as stream:
                    raw = stream.read(20 * 1024 * 1024 + 1)
                truncated |= len(raw) > 20 * 1024 * 1024
                value = raw[:20 * 1024 * 1024].decode(errors='replace')
                log.write_text(redact(value))
                outputs.append(value)
        text, errors = outputs
        if truncated:
            raise GateError(f'Command output exceeded 20 MiB; truncated private logs: {path}')
        if check and proc.returncode:
            from .diagnostics import failure_summary
            stdout, stderr = redact(text), redact(errors)
            raise CommandError(f'{args[0]} exited {proc.returncode}; log {path}\n'
                               f'{failure_summary(stdout, stderr)}', stdout=stdout, stderr=stderr)
        return proc.returncode, text
