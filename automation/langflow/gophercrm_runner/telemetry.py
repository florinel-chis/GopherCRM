"""Durable invocation limits and selected operational reports, not billing estimates."""

import asyncio
import json
import math
import uuid

from .config import BudgetError, CommandError, EnvironmentError, GateError
from .process import redact


def remaining(record, now, *, active_call=None):
    if any(call['status'] == 'running' and call['id'] != active_call
           for call in record.get('provider_invocations', [])):
        raise BudgetError('Unresolved provider invocation after interruption; inspect its process and private '
                          'evidence before any further mutation. Its reservation remains spent.')
    deadline = record.get('deadline')
    if type(deadline) not in {int, float} or not math.isfinite(deadline):
        raise BudgetError('Missing or invalid persisted execution deadline')
    seconds = deadline - now
    if seconds <= 0:
        raise BudgetError('Execution deadline exhausted, including downtime; explicit extension required')
    return seconds


def reserve(record, role, now):
    remaining(record, now)
    if record['provider_calls'] >= record['max_provider_calls']:
        raise BudgetError('Provider call budget exhausted; explicit extension required')
    call = {'id': uuid.uuid4().hex, 'role': role, 'head': record['head'],
            'status': 'running', 'started_at': now, 'pid': None}
    record['provider_calls'] += 1
    record.setdefault('provider_invocations', []).append(call)
    return call


def category(error, stage):
    if isinstance(error, EnvironmentError):
        return 'environment'
    if isinstance(error, BudgetError):
        return 'budget'
    if type(error).__name__ == 'CIFailure' or stage == 'ci':
        return 'ci'
    if stage == 'review':
        return 'review'
    if isinstance(error, CommandError) or stage in {'implementation', 'repair', 'validation'}:
        return 'implementation/test'
    return 'recovery'


async def observe(record, stage, clock, save, operation):
    attempt = {'id': uuid.uuid4().hex, 'stage': stage, 'head': record.get('head'),
               'started_at': clock(), 'outcome': 'running', 'category': None}
    record.setdefault('attempts', []).append(attempt)
    save(record)
    try:
        result = await operation()
    except BaseException as exc:
        attempt.update(outcome='cancelled' if isinstance(exc, asyncio.CancelledError) else 'failed',
                       category=category(exc, stage))
        raise
    else:
        rejected = stage == 'review' and isinstance(result, dict) and result.get('approved') is False
        attempt.update(outcome='rejected' if rejected else 'success', category='review' if rejected else None)
        return result
    finally:
        attempt['ended_at'] = clock()
        attempt['duration_seconds'] = max(0, attempt['ended_at'] - attempt['started_at'])
        save(record)


def interrupted_attempts(record):
    for attempt in record.get('attempts', []):
        if attempt['outcome'] == 'running':
            attempt.update(outcome='interrupted', category='recovery', ended_at=None, duration_seconds=None)


def report(record, preflight, operator, now):
    calls = record.get('provider_invocations', [])
    costs = [c.get('metadata', {}).get('cost', {}) for c in calls]
    known = [c['usd'] for c in costs if c.get('status') == 'provider-reported' and c.get('usd') is not None]
    cost_status = 'provider-reported' if calls and len(known) == len(calls) else ('partial' if known else 'unavailable')
    entries = operator.get('entries', [])
    started, finished = record.get('started_at'), record.get('completed_at', now)
    result = {
        'issue': record.get('issue'), 'status': record.get('stage', 'not_admitted'),
        'completion_target': record.get('completion_target'), 'head': record.get('head'), 'url': record.get('url'),
        'elapsed_seconds': max(0, finished - started) if started is not None else None,
        'deadline': record.get('deadline'), 'repairs': record.get('repairs', 0),
        'provider_calls': {'used': record.get('provider_calls', 0), 'limit': record.get('max_provider_calls')},
        'provider_invocations': [{k: c.get(k) for k in ('id', 'role', 'status', 'head', 'pid',
                                                      'started_at', 'ended_at', 'metadata')} for c in calls],
        'cost': {'status': cost_status, 'total_usd': sum(known) if cost_status == 'provider-reported' else None,
                 'observed_usd': sum(known) if known else None, 'known_calls': len(known), 'total_calls': len(calls)},
        'operator_minutes': {'status': 'operator-reported-partial' if entries else 'unavailable',
                             'minutes': sum(e['minutes'] for e in entries) if entries else None},
        'attempts': preflight.get('attempts', []) + record.get('attempts', []),
        'last_failure': record.get('last_failure'),
        'validation_repair_requests': record.get('validation_repair_requests', []),
        'validation_resumptions': record.get('validation_resumptions', []),
        'documentation': ((record.get('source_snapshot', {}) if record.get('reuse_ticket') else record)
                          .get('review') or {}).get('documentation'),
        'documentation_merge': record.get('documentation_merge'),
    }
    return json.loads(redact(json.dumps(result)))


def operator_entry(minutes, category_name, reason, now):
    if (type(minutes) not in {int, float} or not math.isfinite(minutes) or minutes <= 0
            or category_name not in {'intervention', 'review', 'preparation'}
            or not isinstance(reason, str) or not reason.strip() or len(reason) > 1000):
        raise GateError('Operator notes require positive finite minutes, a category and a short reason')
    return {'minutes': minutes, 'category': category_name, 'reason': redact(reason), 'at': now}


def format_report(value):
    cost = value['cost']
    lines = [f"Issue #{value['issue']}: {value['status']} (target {value['completion_target']})",
             f"Provider CLI calls: {value['provider_calls']['used']}/{value['provider_calls']['limit']}",
             f"Elapsed seconds including downtime: {value['elapsed_seconds']}",
             f"Provider-reported cost: {cost['status']}; total USD {cost['total_usd']} (not a billing estimate)",
             f"Operator minutes: {value['operator_minutes']['minutes']} ({value['operator_minutes']['status']})"]
    lines.extend(f"  {a['stage']}: {a['outcome']} ({a.get('category') or 'no failure'}), "
                 f"{a.get('duration_seconds')} seconds" for a in value['attempts'])
    return '\n'.join(lines)
