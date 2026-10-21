"""Opt-in detached local worker. No daemon, scheduler or automatic restart."""
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
import uuid

from .batches import BatchRunner, batch_id
from .config import Config, GateError
from .process import redact
from .state import Store


def start(config_path, name, *, resume=False):
    config_path = Path(config_path).resolve()
    config = Config.load(config_path)
    runner = BatchRunner(config)
    name = batch_id(name)
    runner.load(name)
    runner.store.batch_id = name
    with runner.store.exclusive():
        fd = os.open(config.state_dir / 'batch-worker.lock', os.O_RDWR | os.O_CREAT, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise GateError('A detached batch worker is already running') from exc
            token = uuid.uuid4().hex
            log = config.state_dir / f'batch-{name}-{token}.log'
            metadata = dict(id=name, token=token, status='starting', started_at=time.time(), log=str(log),
                            control_snapshot=runner.control_snapshot())
            runner.store.write(f'batch-{name}-worker', metadata)
            command = [sys.executable, '-m', 'gophercrm_runner.cli', '_batch-worker',
                       '--config', str(config_path), '--batch', name, '--lock-fd', str(fd), '--token', token]
            if resume:
                command.append('--resume')
            log_fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            ready_read, ready_write = os.pipe()
            try:
                with os.fdopen(log_fd, 'wb') as out:
                    child = subprocess.Popen(command + ['--ready-fd', str(ready_read)],
                        cwd=config.state_dir, stdin=subprocess.DEVNULL, stdout=out, stderr=out,
                        start_new_session=True, pass_fds=(fd, ready_read))
                metadata['pid'] = child.pid
                runner.store.write(f'batch-{name}-worker', metadata)
                # Reap promptly when launched from a long-lived UI process.
                # A short-lived launcher may exit; it never terminates the child.
                threading.Thread(target=child.wait, daemon=True).start()
            except BaseException:
                os.close(ready_write)
                raise
            finally:
                os.close(ready_read)
        finally:
            os.close(fd)  # Child retains the shared open-file-description lock.
    # The worker waits until the parent releases the execution lock. Parent
    # death also closes this pipe; the worker still has its own launch lock.
    os.close(ready_write)
    return metadata


def work(config_path, name, lock_fd, token, ready_fd, *, resume=False):
    try:
        os.read(ready_fd, 1)
    finally:
        os.close(ready_fd)
    config = Config.load(config_path)
    name = batch_id(name)
    store = Store(config.state_dir)
    key = f'batch-{name}-worker'
    metadata = store.read(key)
    expected = os.stat(config.state_dir / 'batch-worker.lock')
    actual = os.fstat(lock_fd)
    if (expected.st_dev, expected.st_ino) != (actual.st_dev, actual.st_ino) or metadata.get('token') != token:
        raise GateError('Detached worker identity does not match the launch reservation')
    # Fail closed for direct invocation without the inherited exclusive lock.
    fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    metadata.update(status='running', pid=os.getpid())
    store.write(key, metadata)
    try:
        from .cli import run_async
        from .flows import run_batch_flow
        result = run_async(run_batch_flow(config_path, name=name, execute=True, resume=resume,
                                         control_snapshot=metadata['control_snapshot']))
        print(json.dumps([item.model_dump(mode='json') for item in result]), flush=True)
        status = BatchRunner(config).status(name)['status']
        metadata.update(status='exited', exit_code=0 if status in {'completed', 'stopped'} else 1,
                        batch_status=status)
    except BaseException as exc:
        metadata.update(status='exited', exit_code=1, reason=redact(str(exc))[-2000:] or type(exc).__name__)
        print(json.dumps({'error': metadata['reason']}), flush=True)
    finally:
        metadata['exited_at'] = time.time()
        store.write(key, metadata)
        os.close(lock_fd)
    return metadata['exit_code']
