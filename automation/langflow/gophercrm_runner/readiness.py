"""Read-only prerequisites shared by CLI doctor and every execution entrypoint."""

import asyncio
import json
import os
from pathlib import Path
import re
import shutil
import tempfile

from .config import EnvironmentError, GateError


VERSION_PROBES = (
    ('git', '--version'), ('gh', '--version'), ('claude', '--version'),
    ('go', 'version'), ('node', '--version'), ('npm', '--version'), ('make', '--version'),
)
APPLICATION_DB_KEYS = {'DB_HOST', 'DB_PORT', 'DB_USER', 'DB_PASSWORD', 'DB_NAME',
                       'DB_DRIVER', 'DB_PATH'}


def verification_environment():
    return {key: value for key, value in os.environ.items() if key not in APPLICATION_DB_KEYS}


def environment_failure(error):
    """Conservative known infrastructure failures; never guess a source fix."""
    return bool(re.search(
        r'ENOSPC|no space left on device|EAI_AGAIN|ENETUNREACH|ECONNREFUSED|'
        r'ERR_MODULE_NOT_FOUND|Executable doesn.t exist|could not resolve host|'
        r'connection refused|connect: connection timed out', str(error), re.I))


def check_disk(config):
    path = config.state_dir
    while not path.exists():
        path = path.parent
    free = shutil.disk_usage(path).free
    if free < config.min_free_disk_mb * 1024 * 1024:
        raise EnvironmentError('environment readiness: insufficient free disk; reclaim only inactive '
                               'rebuildable data, then resume')
    return free


async def doctor(config, commands):
    report = {'ready': False, 'completion_target': config.completion_target, 'versions': {},
              'branch_protection': {'status': 'unknown'}}
    report['free_disk_bytes'] = check_disk(config)
    try:
        config.state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        if config.state_dir.stat().st_mode & 0o077:
            raise EnvironmentError('environment readiness: state directory must be private (0700)')
        with tempfile.TemporaryFile(dir=config.state_dir) as probe:
            probe.write(b'readiness'); probe.flush()
    except OSError as exc:
        raise EnvironmentError('environment readiness: private state is not writable') from exc

    async def probe(args, label):
        try:
            code, output = await commands.run(args, cwd=config.checkout, check=False, timeout=120)
        except (OSError, asyncio.TimeoutError, GateError) as exc:
            raise EnvironmentError(f'environment readiness: {label} unavailable') from exc
        if code:
            raise EnvironmentError(f'environment readiness: {label} failed; inspect private diagnostics')
        return output

    for args in VERSION_PROBES:
        output = await probe(args, args[0])
        if not output.strip():
            raise EnvironmentError(f'environment readiness: missing {args[0]} version')
        report['versions'][args[0]] = output.splitlines()[0][:200]
    for tool, expression, minimum in [('go', r'go(\d+)\.(\d+)', (1, 25)),
                                       ('node', r'v(\d+)\.(\d+)', (20, 0))]:
        match = re.search(expression, report['versions'][tool])
        if not match or tuple(map(int, match.groups())) < minimum:
            raise EnvironmentError(f'environment readiness: unsupported {tool} version')
    await probe(('gh', 'auth', 'status'), 'GitHub authentication')
    try:
        auth = json.loads(await probe(('claude', 'auth', 'status', '--json'), 'Claude authentication'))
    except ValueError as exc:
        raise EnvironmentError('environment readiness: invalid Claude authentication status') from exc
    if not isinstance(auth, dict) or auth.get('loggedIn') is not True:
        raise EnvironmentError('environment readiness: Claude is not authenticated')

    needs_browser = any(c[:2] == ('make', 'e2e') for c in
                        (*config.test_commands, *config.database_test_commands))
    if config.database_test_commands:
        if not config.disposable_database:
            raise EnvironmentError('environment readiness: explicitly acknowledge a disposable database')
        if not os.environ.get('TEST_MYSQL_DSN'):
            raise EnvironmentError('environment readiness: TEST_MYSQL_DSN is required')
    if needs_browser:
        browser = Path(config.chromium_path)
        if not config.chromium_path or not browser.is_absolute() or not browser.is_file() or not os.access(browser, os.X_OK):
            raise EnvironmentError('environment readiness: configure an installed Playwright Chromium executable')
        report['chromium'] = 'installed'
    if config.database_test_commands:
        report['versions']['mysql'] = (await probe(('mysql', '--version'), 'MySQL client')).splitlines()[0][:200]
        output = await probe(('go', 'run', './automation/langflow/probes/mysql'), 'disposable database')
        try:
            result = json.loads(output)
        except ValueError as exc:
            raise EnvironmentError('environment readiness: invalid database probe result') from exc
        if result != {'migration': 'ok', 'e2e': 'ok'}:
            raise EnvironmentError('environment readiness: database connectivity was not confirmed')
        report['database'] = result
        required = {'TestMigrationFresh_FullUpAndDown', 'TestMigrationUpgrade_FromMainAutoMigratedSchema'}
        listed = await probe(('go', 'test', './cmd/migrate', '-list', '^TestMigration'), 'migration test discovery')
        if not required.issubset(set(listed.splitlines())):
            raise EnvironmentError('environment readiness: required migration tests are missing')
    try:
        code, output = await commands.run(
            ('gh', 'api', f'repos/{config.repository}/branches/main/protection'),
            cwd=config.checkout, check=False, timeout=30)
        if code == 0:
            data = json.loads(output)
            if isinstance(data, dict) and 'required_status_checks' in data:
                report['branch_protection'] = {'status': 'available',
                    'required_status_checks': data['required_status_checks'],
                    'enforce_admins': data.get('enforce_admins')}
    except (OSError, ValueError, asyncio.TimeoutError, GateError):
        pass  # Missing metadata is explicitly unknown, never fabricated enforcement.
    report['ready'] = True
    return report
