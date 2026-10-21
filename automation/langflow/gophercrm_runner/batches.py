"""Opt-in, durable admission of a small operator-prepared batch.

The supervisor never invents tasks or executable checks. It holds the existing
execution lock across handoffs and calls the existing ticket/PR executors.
"""

from dataclasses import asdict
import json
from pathlib import Path
import re
import time
import uuid

from . import contracts, evidence
from .config import BudgetError, Config, GateError
from .engine import Runner
from .pull_requests import PullRequestRunner
from .process import redact


MAX_ITEMS = 3


def batch_id(value):
    if not isinstance(value, str) or not re.fullmatch(r'[a-z][a-z0-9-]{0,63}', value):
        raise GateError('Batch ID must be a short lowercase name')
    return value


def read_json(path):
    path = Path(path)
    if not path.is_absolute() or any(p.is_symlink() for p in [path, *path.parents]):
        raise GateError('Batch inputs must be absolute paths without symlinks')
    if not path.is_file() or path.stat().st_size > 2 * 1024 * 1024:
        raise GateError('Missing or oversized batch input')
    try:
        return json.loads(path.read_text())
    except (ValueError, UnicodeError) as exc:
        raise GateError('Invalid batch JSON') from exc


def policy_hash(config):
    return contracts.digest(json.loads(json.dumps(asdict(config), default=str)))


def positive(value):
    return type(value) is int and value > 0


def journal_key(entry):
    return f'issue-{entry["issue"]}' if entry['kind'] == 'issue' else f'completion-pr-{entry["pr"]}'


class BatchRunner:
    def __init__(self, config, commands=None, *, config_path=None, clock=time.time):
        self.config = config
        self.config_path = Path(config_path) if config_path else None
        self.clock = clock
        self.runner = Runner(config, commands, clock=clock)
        self.pr_runner = PullRequestRunner(config, self.runner.commands, clock=clock)
        # Same Store instance permits same-task nesting, never a second worker.
        self.store = self.runner.store
        self.pr_runner.store = self.store
        self.plan = self.record = None

    def plan_name(self, name):
        return f'batch-{batch_id(name)}-plan'

    def record_name(self, name):
        return f'batch-{batch_id(name)}'

    def load(self, name):
        plan = self.store.read(self.plan_name(name))
        if (not plan or plan.get('schema_version') != 1 or plan.get('id') != name
                or contracts.digest({k: v for k, v in plan.items() if k != 'plan_hash'}) != plan.get('plan_hash')):
            raise GateError('Missing or changed prepared batch; prepare a new explicit batch')
        return plan

    async def main(self):
        await self.runner.check_remote()
        await self.runner.command('git', 'fetch', 'origin', 'main')
        fetched = await self.runner.command('git', 'rev-parse', 'origin/main')
        live = await self.runner.command('git', 'ls-remote', 'origin', 'refs/heads/main')
        if not live or live.split()[0] != fetched:
            raise GateError('Main changed during batch inspection')
        return fetched

    async def source_objects(self, paths, base):
        if (not isinstance(paths, list) or not paths or any(not isinstance(p, str) for p in paths)
                or len(set(paths)) != len(paths)):
            raise GateError('Every ticket requires unique source_paths to guard its assumptions')
        result = {}
        for path in paths:
            contracts.safe_path(path)
            if any(c in path for c in '*?[]'):
                raise GateError('source_paths must be literal Git files or directories')
            code, value = await self.runner.run_command(
                ('git', 'rev-parse', '--verify', f'{base}:{path}'), cwd=self.config.checkout, check=False)
            if code not in (0, 128):
                raise GateError('Cannot inspect source path')
            result[path] = value.strip() if code == 0 else None
        return result

    def check_policy(self, plan):
        current = Config.load(self.config_path) if self.config_path else self.config
        if policy_hash(current) != plan['policy_hash'] or policy_hash(self.config) != plan['policy_hash']:
            raise GateError('Batch policy or check registry changed; prepare a new batch')

    async def inspect_pr(self, pr, base):
        ref = f'refs/remotes/origin/batch-pr-{pr["number"]}'
        await self.runner.command('git', 'fetch', 'origin', f'+refs/pull/{pr["number"]}/head:{ref}')
        if await self.runner.command('git', 'rev-parse', ref) != pr['headRefOid']:
            raise GateError('Prepared PR changed during fetch')
        await self.runner.command('git', 'merge-base', '--is-ancestor', base, ref)
        paths = (await self.runner.command('git', 'diff', '--name-only', f'{base}..{ref}')).splitlines()
        if not paths or any(p.startswith(('gocrm-ui/', 'internal/handler/assets/')) for p in paths):
            raise GateError('Batch PR requires a backend change; empty/UI changes are unsupported')
        return contracts.context_hash(await self.runner.context_documents(['AGENTS.md'], base))

    async def prepare(self, specification):
        raw = read_json(specification)
        fields = {'schema_version', 'id', 'initial_base', 'max_items', 'max_provider_calls', 'timeout_seconds', 'entries'}
        if (not isinstance(raw, dict) or set(raw) != fields or type(raw['schema_version']) is not int
                or raw['schema_version'] != 1 or not isinstance(raw['initial_base'], str)
                or not re.fullmatch('[0-9a-f]{40}', raw['initial_base'])):
            raise GateError('Invalid batch specification fields or base')
        name = batch_id(raw['id'])
        if (not isinstance(raw['entries'], list) or not positive(raw['max_items'])
                or not 1 <= len(raw['entries']) <= raw['max_items'] <= MAX_ITEMS
                or not positive(raw['max_provider_calls']) or not positive(raw['timeout_seconds'])):
            raise GateError('Batch requires one to three explicit entries and positive limits')
        if self.config.completion_target != 'merge':
            raise GateError('Batches require the merge completion target')
        with self.store.exclusive():
            if self.store.read(self.plan_name(name)) or self.store.read(self.record_name(name)):
                raise GateError('Batch ID already exists; prepared plans are immutable')
            if self.store.read('active'):
                raise GateError('Finish the active ticket or PR before batch preparation')
            await self.runner.clean(self.config.checkout)
            if await self.main() != raw['initial_base']:
                raise GateError('Initial main does not match the explicitly pinned base')
            entries, seen = [], set()
            for index, item in enumerate(raw['entries']):
                if not isinstance(item, dict):
                    raise GateError('Invalid batch entry')
                if item.get('kind') == 'issue' and set(item) == {'kind', 'issue', 'manifest', 'source_paths'}:
                    self.config.allows(item['issue'])
                    manifest = read_json(item['manifest'])
                    contracts.validate_manifest(self.config, manifest, item['issue'])
                    contracts.check_scope(manifest, manifest['scope_paths'])
                    issue = await self.runner.issue(item['issue'])
                    if issue['state'] != 'OPEN':
                        raise GateError('Batch ticket must still be open')
                    documents = await self.runner.context_documents(manifest['context_refs'], raw['initial_base'])
                    frozen = contracts.create_contract(self.config, manifest, issue, raw['initial_base'], documents)
                    contracts.check_dependency_cycles(self.config.state_dir, item['issue'], manifest['dependencies'])
                    await self.runner.dependency_proof(frozen)
                    entry = dict(item, manifest_hash=contracts.digest(manifest), source_hash=frozen['source_hash'],
                                 context_hash=frozen['context_hash'],
                                 source_objects=await self.source_objects(item['source_paths'], raw['initial_base']))
                    if (self.config.state_dir / 'tasks' / str(item['issue']) / 'contract.json').exists():
                        raise GateError('Batch requires a new ticket without a pre-frozen contract')
                elif (item.get('kind') == 'pr' and set(item) == {'kind', 'pr', 'head'}
                      and positive(item['pr']) and isinstance(item['head'], str)
                      and re.fullmatch('[0-9a-f]{40}', item['head'])):
                    # An existing branch cannot anticipate the preceding squash
                    # merge. MVP permits an explicitly pinned PR only first.
                    if index != 0:
                        raise GateError('An existing PR can only be the first batch entry')
                    pr = await self.pr_runner.pr(item['pr'])
                    self.pr_runner.check_pr(pr, item['head'])
                    if pr['baseRefOid'] != raw['initial_base']:
                        raise GateError('PR base differs from the prepared initial base')
                    entry = dict(item, source_hash=contracts.digest(self.pr_runner.identity(pr)),
                                 context_hash=await self.inspect_pr(pr, raw['initial_base']))
                else:
                    raise GateError('Unsupported batch entry or unknown fields')
                key = journal_key(entry)
                if key in seen or self.store.read(key):
                    raise GateError('Duplicate entry or existing execution; batch adoption is unsupported')
                seen.add(key)
                entries.append(entry)
            plan = dict(raw, entries=entries, policy_hash=policy_hash(self.config))
            plan['plan_hash'] = contracts.digest(plan)
            self.store.write(self.plan_name(name), plan)
            return {'id': name, 'status': 'prepared', 'plan_hash': plan['plan_hash'], 'entries': len(entries),
                    'execution_enabled': False}

    async def inspect_entry(self, entry, base):
        if entry['kind'] == 'pr':
            pr = await self.pr_runner.pr(entry['pr'])
            self.pr_runner.check_pr(pr, entry['head'])
            if pr['baseRefOid'] != base or contracts.digest(self.pr_runner.identity(pr)) != entry['source_hash']:
                raise GateError('Prepared PR identity or base changed')
            if await self.inspect_pr(pr, base) != entry['context_hash']:
                raise GateError('Prepared PR guidance changed')
            return None
        manifest = read_json(entry['manifest'])
        if contracts.digest(manifest) != entry['manifest_hash']:
            raise GateError('Prepared task manifest changed')
        contracts.validate_manifest(self.config, manifest, entry['issue'])
        issue = await self.runner.issue(entry['issue'])
        if issue['state'] != 'OPEN' or contracts.digest(contracts.source(issue)) != entry['source_hash']:
            raise GateError('Prepared issue source changed or issue closed')
        documents = await self.runner.context_documents(manifest['context_refs'], base)
        if contracts.context_hash(documents) != entry['context_hash']:
            raise GateError('Prepared guidance changed')
        if await self.source_objects(entry['source_paths'], base) != entry['source_objects']:
            raise GateError('Prior changes invalidated the prepared source assumptions')
        frozen = contracts.create_contract(self.config, manifest, issue, base, documents)
        await self.runner.dependency_proof(frozen)
        return frozen

    def save(self):
        self.record['updated_at'] = self.clock()
        self.store.write(self.record_name(self.plan['id']), self.record)

    def child(self, allocation):
        record = self.store.read(journal_key(self.plan['entries'][allocation['index']]))
        if record and (record.get('execution_id') != allocation['execution_id']
                       or record.get('batch_id') != self.plan['id']):
            raise GateError('Execution does not belong to this batch allocation')
        return record

    def spent(self):
        total = sum(self.child(a).get('provider_calls', 0) for a in self.record['allocations'])
        if total < self.record.get('provider_calls', 0):
            raise GateError('Batch invocation history decreased or disappeared')
        return total

    def budget(self):
        if self.clock() >= self.record['deadline']:
            raise BudgetError('Batch deadline exhausted, including downtime')
        if self.spent() >= self.plan['max_provider_calls']:
            raise BudgetError('Batch provider-call budget exhausted')

    def bind(self, record):
        allocation = self.record['allocations'][self.record['index']]
        record.update(execution_id=allocation['execution_id'], batch_id=self.plan['id'],
                      started_at=allocation['started_at'], deadline=allocation['deadline'],
                      max_provider_calls=allocation['max_provider_calls'])
        self.check_record(record)

    def check_record(self, record):
        allocation = self.record['allocations'][self.record['index']]
        if (record.get('batch_id') != self.plan['id'] or record['execution_id'] != allocation['execution_id']
                or record['deadline'] != allocation['deadline']
                or record['max_provider_calls'] != allocation['max_provider_calls']
                or record['provider_calls'] > allocation['max_provider_calls']
                or self.spent() > self.plan['max_provider_calls']):
            raise BudgetError('Batch allocation or durable budget changed')

    def progress(self, record):
        self.check_record(record)
        self.record['provider_calls'] = self.spent()
        self.record['current_stage'] = record['stage']
        self.save()

    def status(self, name):
        plan = self.load(name)
        record = self.store.read(self.record_name(name))
        worker = self.store.read(f'batch-{name}-worker')
        if not record:
            return {'id': name, 'status': 'prepared', 'entries': len(plan['entries']),
                    'execution_enabled': False, 'worker': worker}
        self.plan, self.record = plan, record
        result = dict(record)
        result['worker'] = worker
        result['total_entries'] = len(plan['entries'])
        result['max_provider_calls'] = plan['max_provider_calls']
        result['provider_calls'] = self.spent()
        result['remaining_seconds'] = max(0, record['deadline'] - self.clock())
        if record['index'] < len(record['allocations']):
            current = self.child(record['allocations'][record['index']])
            result['current_stage'] = current.get('stage', 'admitting')
            result['current_entry'] = {k: v for k, v in plan['entries'][record['index']].items()
                                       if k in {'kind', 'issue', 'pr'}}
        result['stop_requested'] = self.store.read(f'batch-{name}-control').get('stopped', False)
        return result

    def stop(self, name):
        self.load(name)
        with self.store.control_lock():
            # Also covers a preloaded legacy queue once this batch releases it.
            self.store.write('control', {'stopped': True, 'allowed_issue': self.store.read('active').get('issue'),
                                         'stop_token': uuid.uuid4().hex})
            self.store.write(f'batch-{name}-control', {'stopped': True})
        return {'id': name, 'status': 'stop_requested'}

    def abandon(self, name, reason):
        if not isinstance(reason, str) or not reason.strip() or len(reason) > 1000:
            raise GateError('Abandonment needs a short reason')
        self.store.batch_id = batch_id(name)
        with self.store.exclusive():
            self.plan = self.load(name)
            self.record = self.store.read(self.record_name(name))
            if not self.record or self.record['status'] in {'completed', 'abandoned'}:
                raise GateError('No unfinished batch to abandon')
            for allocation in self.record['allocations']:
                if any(c['status'] == 'running' for c in self.child(allocation).get('provider_invocations', [])):
                    raise GateError('Inspect unresolved provider processes before abandoning the batch')
            self.record.update(status='abandoned', reason=redact(reason))
            self.save()
            self.store.stop_after_current()
            self.store.write('batch-owner', {})
            # An unfinished child remains active with its existing limits. The
            # ordinary recovery path may now handle it; no work is discarded.
            return self.status(name)

    def recover(self, name, reason, *, processes_stopped=False):
        if not isinstance(reason, str) or not reason.strip() or len(reason) > 1000 or processes_stopped is not True:
            raise GateError('Recovery needs a reason and --processes-stopped after inspecting orphaned processes')
        self.store.batch_id = batch_id(name)
        with self.store.exclusive():
            self.plan = self.load(name)
            self.check_policy(self.plan)
            self.record = self.store.read(self.record_name(name))
            if (not self.record or self.record['status'] in {'completed', 'abandoned'}
                    or self.record['index'] >= len(self.record['allocations'])):
                raise GateError('No current batch allocation to recover')
            child = self.child(self.record['allocations'][self.record['index']])
            unresolved = [c for c in child.get('provider_invocations', []) if c['status'] == 'running']
            if not unresolved:
                raise GateError('No unresolved invocation; use normal resume')
            self.check_record(child)
            child.setdefault('operator_recoveries', []).append(dict(at=self.clock(), reason=redact(reason),
                invocation_ids=[c['id'] for c in unresolved], operator_confirmed_processes_stopped=True))
            for call in unresolved:
                call.update(status='operator-resolved', ended_at=None)
            self.store.write(journal_key(self.plan['entries'][self.record['index']]), child)
            self.progress(child)
            return {'id': name, 'status': 'recovered', 'provider_calls': self.spent(), 'resume_required': True}

    async def reconcile(self):
        index = self.record['index']
        if index >= len(self.record['allocations']):
            return False
        allocation = self.record['allocations'][index]
        child = self.child(allocation)
        if not child or not child.get('pr'):
            return False
        entry = self.plan['entries'][index]
        runner = self.runner if entry['kind'] == 'issue' else self.pr_runner
        runner._record = None  # Read-only merge reconciliation survives expiry.
        pr = await runner.pr(child['pr'])
        if pr['state'] != 'MERGED':
            if child['stage'] == 'merged':
                raise GateError('Recorded merge is no longer confirmed by GitHub')
            return False
        if (child['stage'] not in {'merging', 'merged'} or child['base'] != allocation['base']
                or child.get('tested_head') != child['head'] or child.get('reviewed_head') != child['head']
                or pr['headRefOid'] != child['head'] or pr['headRefName'] != child['branch']
                or pr['baseRefName'] != 'main' or not (pr.get('mergeCommit') or {}).get('oid')):
            raise GateError('External or mismatched merge cannot advance the batch')
        if entry['kind'] == 'issue':
            await runner.verify_approval(child)
        else:
            await runner.verify_approval(child, remote_result=True)
        merge = pr['mergeCommit']['oid']
        await runner.command('git', 'fetch', 'origin', 'main')
        parents = (await runner.command('git', 'rev-list', '--parents', '-n', '1', merge)).split()
        tree = await runner.command('git', 'rev-parse', f'{merge}^{{tree}}')
        if parents != [merge, child['base']] or tree != await runner.command('git', 'rev-parse', f'{child["head"]}^{{tree}}'):
            raise GateError('Merge parent/tree does not match this batch candidate')
        await runner.finish(child, pr)
        allocation.update(status='merged', head=child['head'], merge_commit=merge, pr=child['pr'])
        self.record.update(index=index + 1, accepted_base=merge, provider_calls=self.spent(), current_stage='handoff')
        self.save()  # One atomic advancement, also safe after terminal child save.
        return True

    async def preview(self, name):
        self.plan = self.load(name)
        self.check_policy(self.plan)
        record = self.store.read(self.record_name(name))
        base = record['accepted_base'] if record else self.plan['initial_base']
        if await self.main() != base:
            raise GateError('Main differs from the accepted batch base')
        await self.runner.clean(self.config.checkout)
        start = record['index'] if record else 0
        for entry in self.plan['entries'][start:]:
            await self.inspect_entry(entry, base)
        return {'id': name, 'status': 'preview', 'base': base, 'remaining_entries': len(self.plan['entries']) - start,
                'max_provider_calls': self.plan['max_provider_calls'], 'timeout_seconds': self.plan['timeout_seconds']}

    def control_snapshot(self):
        with self.store.control_lock():
            return json.dumps(self.store.read('control').get('stop_token'))

    async def run(self, name, *, execute=False, resume=False, control_snapshot=''):
        if not execute:
            if resume:
                raise GateError('Resume requires explicit execution')
            return await self.preview(name)
        self.store.batch_id = batch_id(name)
        with self.store.exclusive():
            # Capture the request before async preparation/reconciliation. A
            # launcher passes its earlier snapshot through Python/LFX startup.
            try:
                request_token = json.loads(control_snapshot or self.control_snapshot())
            except (ValueError, TypeError) as exc:
                raise GateError('Invalid start control snapshot') from exc
            if request_token is not None and (not isinstance(request_token, str) or not re.fullmatch('[0-9a-f]{32}', request_token)):
                raise GateError('Invalid start control snapshot')
            self.plan = self.load(name)
            self.check_policy(self.plan)
            self.record = self.store.read(self.record_name(name))
            if self.record and self.record['plan_hash'] != self.plan['plan_hash']:
                raise GateError('Execution plan changed')
            if self.record and self.record['status'] == 'completed':
                return self.complete()  # Recover interruption during terminal cleanup.
            if self.record and self.record['status'] == 'abandoned':
                self.store.stop_after_current()
                self.store.write('batch-owner', {})
                return self.status(name)
            if not self.record:
                if self.store.read('active'):
                    raise GateError('An active ticket or PR prevents batch admission')
                await self.preview(name)  # Resolve *all* entries before first model call.
                now = self.clock()
                self.record = dict(schema_version=1, id=name, plan_hash=self.plan['plan_hash'], status='running',
                                   started_at=now, deadline=now + self.plan['timeout_seconds'], index=0,
                                   accepted_base=self.plan['initial_base'], allocations=[], provider_calls=0)
                with self.store.control_lock():
                    control = self.store.read('control')
                    self.store.write('control', dict(control, stopped=True, allowed_issue=None))
                    self.save()
                    self.store.write('batch-owner', {'id': name, 'plan_hash': self.plan['plan_hash']})
                    if control.get('stop_token') == request_token:
                        self.store.write('control', dict(control, stopped=False, allowed_issue=None))
            else:
                owner = self.store.read('batch-owner')
                if owner and owner != {'id': name, 'plan_hash': self.plan['plan_hash']}:
                    raise GateError('Batch ownership changed')
                self.store.write('batch-owner', {'id': name, 'plan_hash': self.plan['plan_hash']})
            self.runner.batch_context = self.pr_runner.batch_context = self
            try:
                # Reconcile a lost response before time, stop or readiness gates.
                await self.reconcile()
                if self.record['index'] == len(self.plan['entries']):
                    return self.complete()
                if self.record['status'] in {'failed', 'stopped'} and not resume:
                    return self.status(name)
                if resume:
                    with self.store.control_lock():
                        control = self.store.read('control')
                        if control.get('stop_token') == request_token:
                            self.store.write(f'batch-{name}-control', {'stopped': False})
                            self.store.write('control', dict(control, stopped=False, allowed_issue=None))
                    self.record['status'] = 'running'
                    self.save()
                while self.record['index'] < len(self.plan['entries']):
                    index = self.record['index']
                    entry = self.plan['entries'][index]
                    admitted = index < len(self.record['allocations'])
                    if not admitted:
                        if self.store.stopped() or self.store.read(f'batch-{name}-control').get('stopped'):
                            self.record.update(status='stopped', reason='Stop requested before next admission')
                            self.save()
                            return self.status(name)
                        self.budget()
                        self.check_policy(self.plan)
                        await self.runner.clean(self.config.checkout)
                        if await self.main() != self.record['accepted_base']:
                            raise GateError('Unrelated main update; batch handoff refused')
                        await self.inspect_entry(entry, self.record['accepted_base'])
                        await self.runner.doctor()
                        self.budget()
                        if self.store.read(journal_key(entry)) or self.store.read('active'):
                            raise GateError('Another execution appeared before batch admission')
                        now = self.clock()
                        allocation = dict(index=index, execution_id=uuid.uuid4().hex, status='admitting',
                            base=self.record['accepted_base'], started_at=now,
                            deadline=min(self.record['deadline'], now + self.config.issue_timeout_seconds),
                            max_provider_calls=min(self.config.max_provider_calls,
                                                   self.plan['max_provider_calls'] - self.spent()))
                        with self.store.control_lock():
                            if self.store.stopped() or self.store.read(f'batch-{name}-control').get('stopped'):
                                self.record.update(status='stopped', reason='Stop requested during preflight')
                                self.save()
                                return self.status(name)
                            self.record['allocations'].append(allocation)
                            self.save()
                            self.store.write('active', {entry['kind']: entry[entry['kind']]})
                    allocation = self.record['allocations'][index]
                    if admitted:
                        # The allocation is the durable admission. Restore the
                        # secondary active marker after a crash between writes.
                        with self.store.control_lock():
                            active = {entry['kind']: entry[entry['kind']]}
                            if self.store.read('active') not in ({}, active):
                                raise GateError('Another execution owns the active marker')
                            self.store.write('active', active)
                            control = self.store.read('control')
                            if control.get('stopped') and entry['kind'] == 'issue':
                                self.store.write('control', dict(control, allowed_issue=entry['issue']))
                    child = self.child(allocation)
                    if self.clock() >= allocation['deadline']:
                        raise BudgetError('Batch or ticket deadline exhausted')
                    if not child:
                        if await self.main() != allocation['base']:
                            raise GateError('Main changed after admission')
                        frozen = await self.inspect_entry(entry, allocation['base'])
                        if entry['kind'] == 'issue':
                            path = self.config.state_dir / 'tasks' / str(entry['issue']) / 'contract.json'
                            if path.exists():
                                if contracts.load_contract(self.config, entry['issue']) != frozen:
                                    raise GateError('An existing contract differs from this admission')
                            else:
                                evidence.private_bytes(path, json.dumps(frozen, indent=2).encode())
                            allocation['contract_hash'] = frozen['contract_hash']
                            self.save()
                    else:
                        self.check_record(child)
                    # Existing executors own all validation, repair, CI and merge
                    # decisions. Never normalize a rejected verdict here.
                    if entry['kind'] == 'issue':
                        result = await self.runner.run_issue(entry['issue'], execute=True)
                    else:
                        result = await self.pr_runner.complete_pr(entry['pr'], entry['head'], execute=True)
                    if result['status'] != 'merged':
                        raise GateError(f'Entry stopped without a verified merge: {result["status"]}')
                    if not await self.reconcile():
                        raise GateError('Executor result lacks a confirmed batch merge')
                return self.complete()
            except BaseException as exc:
                self.record.update(status='failed', reason=redact(str(exc))[-2000:] or type(exc).__name__)
                self.record.setdefault('failures', []).append({'at': self.clock(), 'reason': self.record['reason']})
                self.save()
                raise
            finally:
                self.runner.batch_context = self.pr_runner.batch_context = None

    def complete(self):
        self.record.setdefault('completed_at', self.clock())
        self.record.update(status='completed', current_stage='completed')
        self.save()
        self.store.stop_after_current()
        self.store.write('batch-owner', {})
        return self.status(self.plan['id'])
