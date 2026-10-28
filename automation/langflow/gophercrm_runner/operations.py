"""Read-only recovery, validation previews and comparable historical measurements."""

import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import statistics
import subprocess
import time

from . import contracts, evidence, telemetry, validation
from .config import GateError
from .gates import touches_database
from .process import redact
from .resources import liveness
from .state import Store


JOURNAL = re.compile(r'(?:issue-[0-9]+|completion-pr-[0-9]+)\.json$')
TERMINAL = {'merged', 'pr_ready', 'abandoned', 'external_merge'}


def runtime_identity(checkout):
    package = Path(__file__).parent
    source = Path(checkout) / 'automation/langflow/gophercrm_runner'
    hashes = lambda root: {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                           for p in sorted(root.glob('*.py'))}
    installed = hashes(package)
    try:
        version = importlib.metadata.version('gophercrm-remediation-runner')
    except importlib.metadata.PackageNotFoundError:
        version = 'source checkout'
    expected = hashes(source) if source.is_dir() else None
    return {'version': version, 'module_digest': evidence.sha(json.dumps(installed, sort_keys=True).encode()),
            'matches_checkout': installed == expected if expected is not None else None,
            'mismatched_modules': sorted(k for k in set(installed) | set(expected or {})
                                         if expected is not None and installed.get(k) != expected.get(k))}


def journals(root, *, include_siblings=False):
    root = Path(root)
    directories = [root]
    if include_siblings:
        directories += [p for p in sorted(root.iterdir()) if p.is_dir() and not p.is_symlink()]
    count = 0
    for directory in directories:
        for path in sorted(directory.glob('*.json')):
            if not JOURNAL.fullmatch(path.name) or path.is_symlink():
                continue
            count += 1
            if count > 2000:
                raise GateError('Too many journals; narrow the history root')
            try:
                yield path, json.loads(path.read_text())
            except (OSError, ValueError):
                continue


def measurements(root, *, include_siblings=False):
    runs, seen = [], set()
    for path, record in journals(root, include_siblings=include_siblings):
        identity = record.get('execution_id') or str(path)
        if identity in seen:
            continue
        seen.add(identity)
        stages = {}
        failures = {}
        for attempt in record.get('attempts', []):
            duration = attempt.get('duration_seconds')
            if duration is not None:
                stages[attempt['stage']] = stages.get(attempt['stage'], 0) + duration
            if attempt.get('category'):
                category = attempt['category']
                failures[category] = failures.get(category, 0) + 1
        plan = record.get('validation_plan', {})
        runs.append({'execution_id': identity, 'issue': record.get('issue'), 'pr': record.get('pr'),
                     'profile': plan.get('profile', 'unknown'), 'status': record.get('stage'),
                     'head': record.get('head'), 'policy': record.get('policy'),
                     'stage_seconds': stages, 'failures': failures,
                     'reused_checks': sum(len(r.get('reused_checks', [])) for r in record.get('validation_resumptions', [])),
                     'provider_calls': record.get('provider_calls')})
    groups = {}
    for run in runs:
        if run['status'] not in {'merged', 'pr_ready'} or 'validation' not in run['stage_seconds']:
            continue
        key = (run['profile'], run['policy'])
        groups.setdefault(key, []).append(run['stage_seconds']['validation'])
    return {'runs': runs, 'groups': [
        {'profile': profile, 'policy': policy, 'sample_count': len(values),
         'median_validation_seconds': statistics.median(values),
         'minimum_validation_seconds': min(values), 'maximum_validation_seconds': max(values)}
        for (profile, policy), values in groups.items()],
        'limitations': 'Stage wall times include failures and downtime. Groups share profile/policy, '
                      'but task complexity and environments can differ; these are not causal benchmarks.'}


def validation_preview(config, paths, *, manifest=None, history_root=None, selected=None):
    selected = selected or validation.plan(paths, full=config.validation_mode != 'contextual')
    if any(not (config.checkout / 'gocrm-ui' / spec).is_file() for spec in selected['specs']):
        selected = validation.plan(paths, full=True)
        selected['reasons'].append('selected spec is missing')
    checks = [list(c) for c in validation.core_commands(config.test_commands, selected, explicit_full=config.validation_mode == 'full')]
    acceptance = []
    if manifest:
        for check in manifest.get('acceptance', []):
            key = check['verification']['check_id']
            if key not in config.acceptance_checks:
                raise GateError('Manifest references an unregistered acceptance check')
            if key not in acceptance:
                acceptance.append(key)
                checks.append(config.acceptance_checks[key]['argv'])
    # A preparation preview conservatively displays the configured database
    # matrix. Execution still computes the authoritative plan from the diff.
    requires_database = (touches_database(paths) and selected['profile'] != 'auth') or bool(
        manifest and manifest.get('validation_profile') in contracts.DATABASE_PROFILES) or (
        selected['profile'] == 'full' and config.validation_mode != 'legacy')
    database = config.database_test_commands if requires_database else ()
    checks.extend(list(validation.browser_command(c, selected)) for c in database)
    if not database and config.validation_mode == 'contextual':
        checks.append(list(validation.browser_command(('make', 'e2e', 'E2E_DB_DRIVER=sqlite'), selected)))
    samples = measurements(history_root or config.state_dir)['groups']
    from .engine import Runner
    policy = Runner(config).policy
    estimates = [g for g in samples if g['profile'] == selected['profile'] and g['policy'] == policy]
    return {'selection': selected, 'commands': checks, 'acceptance_ids': sorted(acceptance),
            'estimate': estimates[0] if estimates else {'status': 'unknown', 'sample_count': 0},
            'basis': 'Provisional paths; the committed diff and frozen contract determine actual gates.',
            'missing_database_policy': requires_database and not database}


def git_state(path):
    if not path or not Path(path).is_dir():
        return {'status': 'missing'}
    try:
        env = dict(os.environ, GIT_OPTIONAL_LOCKS='0')
        head = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=path, capture_output=True, text=True, timeout=5, env=env)
        status = subprocess.run(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=path,
                                capture_output=True, text=True, timeout=5, env=env)
        if head.returncode or status.returncode:
            return {'status': 'unavailable'}
        return {'status': 'clean' if not status.stdout else 'dirty', 'head': head.stdout.strip()}
    except (OSError, subprocess.TimeoutExpired):
        return {'status': 'unavailable'}


def handoff(config, *, issue=None, pr=None, batch=None):
    store, now = Store(config.state_dir), time.time()
    batch = batch or store.read('batch-owner').get('id')
    if batch:
        from .batches import batch_id
        batch_id(batch)
    if issue is None and pr is None:
        active = store.read('active')
        issue, pr = active.get('issue'), active.get('pr')
    if issue is not None and (type(issue) is not int or issue <= 0) or pr is not None and (type(pr) is not int or pr <= 0):
        raise GateError('Report target must be a positive issue or PR number')
    key = f'issue-{issue}' if issue is not None else f'completion-pr-{pr}'
    record = store.read(key) if issue is not None or pr is not None else {}
    worker = store.read(f'batch-{batch}-worker') if batch else {}
    heartbeat = store.read(f'batch-{batch}-heartbeat') if batch else {}
    worker = dict(worker, **{k: heartbeat[k] for k in ('process_identity',) if k in heartbeat
                            and heartbeat.get('token') == worker.get('token')})
    summary = telemetry.report(record, {}, store.read(f'operator-{issue}'), now)
    summary.update(target_kind='pr' if pr is not None else 'issue',
                   pr=pr or record.get('pr'), batch=batch, observed_at=now,
                   runtime=runtime_identity(config.checkout), policy=record.get('policy'),
                   source=git_state(record.get('worktree')), validation_checkout=git_state(record.get('validation_worktree')),
                   worker=liveness(worker, now),
                   heartbeat_at=heartbeat.get('at') if heartbeat.get('token') == worker.get('token') else None,
                   pending_journal_update=bool(store.read('pending-update')),
                   validation_plan=record.get('validation_plan'), receipts=[])
    summary['current_check'] = record.get('current_check')
    summary['validation_forecast'] = record.get('validation_forecast')
    for check_id, reference in record.get('receipts', {}).items():
        try:
            raw = json.loads(evidence.safe_artifact(config.state_dir, reference['path']).read_text())
            evidence.verify_receipt(config.state_dir, record, reference, check_id, raw['argv'])
            integrity = 'verified'
        except (GateError, OSError, ValueError, KeyError) as exc:
            integrity = 'unavailable: ' + str(exc)[:200]
        summary['receipts'].append({'check': check_id, 'integrity': integrity})
    summary['receipt_scope'] = 'Integrity only; resume rechecks source, policy, commands and named-test coverage.'
    resources = sorted((config.state_dir / 'logs').glob('*.resources.json'),
                       key=lambda p: p.stat().st_mtime, reverse=True)
    summary['latest_resources'] = None
    if resources:
        try:
            data = json.loads(resources[0].read_text())
            summary['latest_resources'] = {k: v for k, v in data.items() if k != 'samples'}
        except (OSError, ValueError):
            summary['latest_resources'] = {'status': 'unavailable'}
    unresolved = any(c.get('status') == 'running' for c in record.get('provider_invocations', []))
    terminal = record.get('stage') in TERMINAL
    expired = record.get('deadline', now + 1) <= now
    summary['active_stop_reason'] = None if terminal else record.get('last_error')
    summary['next_action'] = (
        'Recover the pending journal transaction under its original owner.' if summary['pending_journal_update'] else
        'Observe the verified worker; do not start a duplicate.' if summary['worker']['status'] == 'verified-running' else
        'Inspect orphaned processes, then record recovery without refunding calls.' if unresolved else
        'Execution is terminal; inspect completion and documentation evidence before admitting another item.' if terminal else
        'Inspect the failure, explicitly extend the batch budget, then resume the same batch.' if expired and batch else
        'Inspect the failure and explicitly extend the existing execution budget.' if expired else
        'Restore failed prerequisites or diagnose the recorded failure, then resume the original execution.' if record else
        'No active execution. Prepare one explicitly selected dependency-ready ticket.')
    return json.loads(redact(json.dumps(summary)))


def markdown(value):
    label = (f"PR #{value.get('pr')}" if value.get('target_kind') == 'pr' or not value.get('issue')
             else f"Issue #{value['issue']}")
    lines = [f"# {label} workflow report", '',
             f"Status: **{value.get('status')}**. Candidate: `{value.get('head')}`.", '',
             f"Next action: {value.get('next_action', 'Inspect the recorded completion evidence.')}", '',
             f"Provider calls: {value['provider_calls']['used']}/{value['provider_calls']['limit']}. "
             f"Repair rounds: {value.get('repairs', 0)}.", '',
             f"Worker: {value.get('worker', {}).get('status', 'unknown')}. "
             f"Source: {value.get('source', {}).get('status', 'unknown')}. "
             f"Installed modules match checkout: {value.get('runtime', {}).get('matches_checkout')}.", '',
             '| Stage | Result | Seconds |', '| --- | --- | ---: |']
    for a in value.get('attempts', []):
        duration = a.get('duration_seconds')
        lines.append(f"| {a['stage']} | {a['outcome']} | {round(duration, 1) if duration is not None else 'unknown'} |")
    if value.get('receipts'):
        lines += ['', 'Saved receipts (integrity only; resume rechecks eligibility):', '']
        lines.extend(f"- `{r['check']}`: {r['integrity']}" for r in value['receipts'])
    if value.get('active_stop_reason'):
        lines += ['', 'Stop reason: ' + value['active_stop_reason'].replace('\n', ' ')[:1000]]
    lines += ['', 'Recorded evidence is scoped to this candidate. Missing checks and existing skips are not passes.']
    return '\n'.join(lines)
