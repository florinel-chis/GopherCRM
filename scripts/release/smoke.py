#!/usr/bin/env python3
"""Boot the exact packaged production binary against a fresh temporary SQLite DB."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import urlopen
import tarfile

from release import BUNDLE, verify


def main():
    folder = Path('release-artifacts')
    data = verify(folder, revision=os.environ['REVISION'])
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        with tarfile.open(folder / BUNDLE) as archive:
            archive.extractall(root, filter='data')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        env = {'PATH': os.environ['PATH'], 'DB_DRIVER': 'sqlite', 'DB_PATH': str(root / 'smoke.db'),
               'SERVER_HOST': '127.0.0.1', 'SERVER_PORT': str(port), 'SERVER_MODE': 'production',
               'JWT_SECRET': secrets.token_urlsafe(48), 'AEO_SCHEDULE_ENABLED': 'false',
               'API_PREFIX': '/api/v1', 'LOG_LEVEL': 'error'}
        with (root / 'smoke.log').open('w+') as log:
            process = subprocess.Popen([str(root / 'bin/gophercrm')], cwd=root, env=env,
                                       stdout=log, stderr=subprocess.STDOUT)
            try:
                for _ in range(120):
                    if process.poll() is not None:
                        raise RuntimeError('Packaged API exited during startup')
                    try:
                        with urlopen(f'http://127.0.0.1:{port}/health', timeout=1) as response:
                            health = json.load(response)
                        payload = health.get('data', {})
                        if (health.get('success') is not True or payload.get('status') != 'healthy'
                                or payload.get('revision') != data['revision']):
                            raise RuntimeError('Packaged API health/revision mismatch')
                        break
                    except (URLError, TimeoutError):
                        time.sleep(0.5)
                else:
                    raise RuntimeError('Packaged API failed to become healthy')
                try:
                    with urlopen(f'http://127.0.0.1:{port}/api/v1/tickets', timeout=5):
                        raise RuntimeError('Packaged API allowed anonymous ticket access')
                except HTTPError as exc:
                    if exc.code != 401:
                        raise
            except BaseException:
                log.flush()
                log.seek(0)
                print(log.read()[-8000:])
                raise
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
    print('Packaged production API health, exact revision and anonymous access boundary passed')


if __name__ == '__main__':
    main()
