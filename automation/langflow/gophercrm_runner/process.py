"""Bounded subprocess execution with cancellation and private diagnostic logs."""

import asyncio
from contextlib import nullcontext
from datetime import datetime, timezone
import os
from pathlib import Path
import re
import signal
import time
import uuid

from .config import BudgetError, CommandError, GateError
from .resources import Samples, heavy_command, heavy_lease


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
                  check: bool = True, env: dict[str, str] | None = None, on_start=None,
                  wall_deadline=None, minimum_free_bytes=0, wall_clock=time.time) -> tuple[int, str]:
        self.log_dir.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.log_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S')
        path = self.log_dir / f'{stamp}-{uuid.uuid4().hex[:8]}.log'
        error_path = path.with_suffix('.stderr.log')
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        error_fd = os.open(error_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        # Stream to disk, rather than retaining unlimited agent/test output in RAM.
        try:
            with os.fdopen(fd, 'wb') as output, os.fdopen(error_fd, 'wb') as errors, \
                    (heavy_lease() if heavy_command(args) else nullcontext()):
                proc = await asyncio.create_subprocess_exec(
                    *args, cwd=cwd, stdin=asyncio.subprocess.PIPE,
                    stdout=output, stderr=errors, start_new_session=True, env=env,
                )
                try:
                    if on_start is not None:
                        on_start(proc.pid)
                    await wait_command(proc, input_text, timeout, wall_deadline,
                        Samples(path.with_suffix('.resources.json'), cwd, proc.pid, minimum_free_bytes)
                        if heavy_command(args) else None, clock=wall_clock)
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


async def wait_command(proc, input_text, timeout, deadline, samples=None, *, clock=time.time,
                       monotonic=time.monotonic, interval=2):
    """Recheck wall time after sleep/resume as well as the event loop's timeout."""
    started = clock()
    limit = min(started + timeout, deadline) if deadline is not None else started + timeout
    monotonic_limit = monotonic() + timeout
    communicate = asyncio.create_task(proc.communicate(input_text.encode() if input_text is not None else None))
    next_sample = started
    try:
        while True:
            now = clock()
            monotonic_remaining = monotonic_limit - monotonic()
            if deadline is not None and now >= deadline:
                raise BudgetError('Execution wall-clock deadline exhausted during command')
            if now >= limit or monotonic_remaining <= 0:
                raise asyncio.TimeoutError()
            if samples and now >= next_sample:
                samples.sample()
                next_sample = now + 15
            done, _ = await asyncio.wait({communicate}, timeout=min(interval, limit - now, monotonic_remaining))
            if done:
                await communicate
                if deadline is not None and clock() >= deadline:
                    raise BudgetError('Command finished after the execution wall-clock deadline')
                if samples:
                    samples.sample()
                return
    finally:
        if not communicate.done():
            communicate.cancel()
        await asyncio.gather(communicate, return_exceptions=True)
