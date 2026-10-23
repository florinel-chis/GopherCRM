"""Bounded host observations and one heavy command per user on this machine."""

from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import time

from .config import EnvironmentError


def process_identity(pid):
    if type(pid) is not int or pid <= 0:
        return None
    try:
        if platform.system() == 'Linux':
            value = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
            return 'linux:' + Path('/proc/sys/kernel/random/boot_id').read_text().strip() + ':' + value[19]
        result = subprocess.run(['ps', '-p', str(pid), '-o', 'lstart='], capture_output=True,
                                text=True, timeout=2)
        return result.stdout.strip() if result.returncode == 0 and result.stdout.strip() else None
    except (OSError, ValueError, IndexError, subprocess.TimeoutExpired):
        return None


def liveness(metadata, now=None):
    result = {'observed_at': time.time() if now is None else now, 'pid': metadata.get('pid')}
    actual = process_identity(metadata.get('pid'))
    expected = metadata.get('process_identity')
    result['status'] = ('verified-running' if actual and expected == actual else
                        'identity-mismatch' if actual and expected else
                        'unknown-identity' if actual else 'not-observed')
    return result


def heavy_command(args):
    return bool(args and (args[0] in {'npm', 'npx', 'go'} or
                          args[0] == 'make' and any(a.startswith(('verify', 'e2e', 'build')) for a in args[1:])))


@contextmanager
def heavy_lease(root=None):
    """A separate shared lock covers distinct state directories, including cleanup."""
    root = Path(root) if root else Path('/tmp').resolve() / f'gophercrm-heavy-{os.getuid()}'
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    if root.is_symlink() or root.stat().st_uid != os.getuid() or root.stat().st_mode & 0o077:
        raise EnvironmentError('Heavy-command lock directory must be private and owned by the current user')
    fd = os.open(root / 'execution.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise EnvironmentError('Another heavy runner command or dependency cleanup is active on this machine') from exc
        yield
    finally:
        os.close(fd)


def snapshot(path, pid=None):
    result = {'at': time.time(), 'free_disk_bytes': shutil.disk_usage(path).free,
              'load_average': list(os.getloadavg()), 'memory': {'status': 'unavailable'},
              'owned_processes': [], 'process_observation': 'unavailable'}
    try:
        if platform.system() == 'Linux':
            values = dict(re.findall(r'^(\w+):\s+(\d+)', Path('/proc/meminfo').read_text(), re.M))
            result['memory'] = {'status': 'observed', 'available_bytes': int(values['MemAvailable']) * 1024,
                                'total_bytes': int(values['MemTotal']) * 1024,
                                'swap_used_bytes': (int(values['SwapTotal']) - int(values['SwapFree'])) * 1024}
        elif platform.system() == 'Darwin':
            output = subprocess.run(['vm_stat'], capture_output=True, text=True, timeout=2)
            page = int(re.search(r'page size of (\d+) bytes', output.stdout)[1])
            pages = {k: int(v) for k, v in re.findall(r'^([^:]+):\s+(\d+)\.', output.stdout, re.M)}
            result['memory'] = {'status': 'observed-pages', 'page_size': page,
                                'free_bytes': pages.get('Pages free', 0) * page,
                                'inactive_bytes': pages.get('Pages inactive', 0) * page,
                                'compressed_bytes': pages.get('Pages occupied by compressor', 0) * page}
        if pid:
            output = subprocess.run(['ps', '-axo', 'pid=,ppid=,rss=,comm='], capture_output=True,
                                    text=True, timeout=2)
            if output.returncode == 0:
                rows = []
                for line in output.stdout.splitlines():
                    fields = line.split(None, 3)
                    if len(fields) == 4:
                        rows.append({'pid': int(fields[0]), 'parent': int(fields[1]),
                                     'rss_bytes': int(fields[2]) * 1024,
                                     'executable': Path(fields[3]).name})
                owned = {pid}
                while True:
                    children = {r['pid'] for r in rows if r['parent'] in owned}
                    if children <= owned:
                        break
                    owned |= children
                result['owned_processes'] = [r for r in rows if r['pid'] in owned][:128]
                result['process_observation'] = 'observed'
    except (OSError, KeyError, ValueError, TypeError, subprocess.TimeoutExpired):
        pass  # Unknown telemetry never becomes a fabricated healthy result.
    return result


class Samples:
    """Keep first/latest observations and peaks, with a bounded tail for diagnosis."""
    def __init__(self, path, cwd, pid, minimum_free_bytes=0):
        self.path, self.cwd, self.pid = Path(path), Path(cwd), pid
        self.minimum_free_bytes = minimum_free_bytes
        self.value = {'schema_version': 1, 'pid': pid, 'process_identity': process_identity(pid),
                      'samples': [], 'count': 0, 'minimum_free_disk_bytes': None, 'peak_owned_rss_bytes': 0}

    def sample(self):
        current = snapshot(self.cwd, self.pid)
        self.value.setdefault('first', current)
        self.value['latest'] = current
        self.value['count'] += 1
        self.value['samples'] = (self.value['samples'] + [current])[-60:]
        free = current['free_disk_bytes']
        previous = self.value['minimum_free_disk_bytes']
        self.value['minimum_free_disk_bytes'] = free if previous is None else min(free, previous)
        self.value['peak_owned_rss_bytes'] = max(self.value['peak_owned_rss_bytes'],
                                               sum(p['rss_bytes'] for p in current['owned_processes']))
        temp = self.path.with_suffix('.tmp')
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, 'w') as stream:
            json.dump(self.value, stream)
        os.replace(temp, self.path)
        if free < self.minimum_free_bytes:
            raise EnvironmentError('Free disk fell below the configured floor during validation; inspect resource samples')
