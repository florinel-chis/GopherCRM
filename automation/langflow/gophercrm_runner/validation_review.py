"""Explicit operator inspection of a committed validation plan; never a test waiver."""

import json
from pathlib import Path
from . import evidence
from .config import EnvironmentError, GateError


def plan_identity(record, checks):
    payload = {key: record.get(key) for key in
               ('execution_id', 'base', 'head', 'policy', 'contract_hash', 'context_hash', 'validation_plan')}
    payload['checks'] = checks  # Includes exact named-test identities, not just commands.
    return evidence.sha(json.dumps(payload, sort_keys=True).encode())


def require_confirmation(runner, record, checks):
    digest = plan_identity(record, checks)
    record['validation_inspection'] = {'head': record['head'], 'plan_hash': digest,
                                       'selection': record['validation_plan'],
                                       'checks': checks}
    runner.save(record)
    if not any(item.get('plan_hash') == digest for item in record.get('validation_confirmations', [])):
        raise EnvironmentError('Committed validation plan awaits operator inspection; inspect the diff and '
                               'validation_inspection, then use confirm-validation and resume. No checks ran.')


async def confirm(config, number, *, head, plan_hash, reason, batch=None, pull_request=False):
    from .engine import Runner
    from .pull_requests import PullRequestRunner
    runner = (PullRequestRunner if pull_request else Runner)(config)
    if not config.validation_review_required or not reason.strip() or len(reason) > 1000:
        raise GateError('Confirmation requires enabled inspection and a short reason')
    if batch:
        from .batches import batch_id
        runner.store.batch_id = batch_id(batch)
    with runner.store.exclusive():
        key = f'completion-pr-{number}' if pull_request else f'issue-{number}'
        record = runner.store.read(key)
        if (record.get('stage') != 'validate' or record.get('head') != head
                or record.get('policy') != runner.policy
                or any(c.get('status') == 'running' for c in record.get('provider_invocations', []))):
            raise GateError('No stopped matching candidate awaiting validation')
        owner = runner.store.read('batch-owner')
        if owner:
            state = runner.store.read('batch-' + batch)
            allocations = state.get('allocations', [])
            index = state.get('index', -1)
            if (state.get('status') not in {'failed', 'stopped'} or index < 0 or index >= len(allocations)
                    or owner.get('plan_hash') != state.get('plan_hash')
                    or allocations[index].get('execution_id') != record.get('execution_id')):
                raise GateError('Batch is not stopped at a current allocation')
            # The active marker is set by the admitted child under the same lock.
        expected = {'pr' if pull_request else 'issue': number}
        if runner.store.read('active') != expected:
            raise GateError('Confirmation must target the active execution')
        await runner.check_inputs(record)
        await runner.clean(Path(record['worktree']))
        if await runner.head(Path(record['worktree'])) != head:
            raise GateError('Candidate changed before confirmation')
        paths = await runner.validate_agent_paths(record)
        checks = await runner.validation_checks(record, paths)
        record['validation_plan'] = runner.validation_selection(record, paths)
        digest = plan_identity(record, checks)
        if digest != plan_hash or (record.get('validation_inspection') or {}).get('plan_hash') != digest:
            raise GateError('Inspected plan changed; resume to record its current identity')
        record.setdefault('validation_confirmations', []).append(
            {'head': head, 'plan_hash': digest, 'reason': reason, 'at': runner.clock()})
        runner.save(record)
        return dict(head=head, plan_hash=digest, confirmed=True, resume_required=True)
