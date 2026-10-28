"""CLI that executes the same Langflow graph imported into the visual editor."""

import argparse
import asyncio
import json
import signal
from pathlib import Path


def run_async(operation):
    """SIGTERM follows the same cancellation/child cleanup path as Ctrl-C."""
    async def supervised():
        loop = asyncio.get_running_loop()
        task = asyncio.current_task()
        loop.add_signal_handler(signal.SIGTERM, task.cancel)
        try:
            return await operation
        finally:
            loop.remove_signal_handler(signal.SIGTERM)
    return asyncio.run(supervised())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    export = sub.add_parser('export', help='Generate the importable Langflow workflow')
    export.add_argument('path', type=Path)
    export.add_argument('--pr', action='store_true', help='Export the existing-PR completion workflow')
    export.add_argument('--batch', action='store_true', help='Export the prepared-batch workflow')
    export.add_argument('--analysis', action='store_true', help='Export the feature analysis workflow')
    analyze = sub.add_parser('analyze', help='Run the feature analysis workflow: one request in, one docs/specs document out')
    analyze.add_argument('--config', type=Path, required=True)
    analyze.add_argument('--wish', default='', help='The feature request; blank in a refinement')
    analyze.add_argument('--link', action='append', default=[], help='Reference link (repeatable, at most ten)')
    analyze.add_argument('--refine', default='', metavar='RUN_ID', help='Refine the document of this previous run')
    analyze.add_argument('--comments-file', type=Path, help='Refinement comments, blank lines between comments')
    analyze.add_argument('--from', dest='refine_from', default='research',
                         choices=('intake', 'research', 'analysis', 'specification'),
                         help='Refinement only: first stage to re-run (earlier stages are inherited)')
    for name in ('prepare-batch', 'run-batch', 'start-batch', 'status-batch', 'stop-batch', 'abandon-batch', 'recover-batch', 'extend-batch-budget', '_batch-worker'):
        command = sub.add_parser(name)
        command.add_argument('--config', type=Path, required=True)
        if name == 'prepare-batch':
            command.add_argument('--spec', type=Path, required=True)
        else:
            command.add_argument('--batch', required=True)
        if name in {'run-batch', 'start-batch', '_batch-worker'}:
            command.add_argument('--resume', action='store_true')
        if name == 'run-batch':
            command.add_argument('--execute', action='store_true', help='Preview is the default')
        if name in {'abandon-batch', 'recover-batch', 'extend-batch-budget'}:
            command.add_argument('--reason', required=True)
        if name == 'extend-batch-budget':
            command.add_argument('--calls', type=int, default=0)
            command.add_argument('--seconds', type=int, default=0)
        if name == 'recover-batch':
            command.add_argument('--processes-stopped', action='store_true')
        if name == '_batch-worker':
            command.add_argument('--lock-fd', type=int, required=True)
            command.add_argument('--ready-fd', type=int, required=True)
            command.add_argument('--token', required=True)
    complete = sub.add_parser('complete-pr', help='Review, validate and merge one explicitly pinned existing PR')
    complete.add_argument('--config', type=Path, required=True)
    complete.add_argument('--pr', type=int, required=True)
    complete.add_argument('--head', required=True, help='Full current PR head SHA')
    complete.add_argument('--execute', action='store_true', help='Run to merge; defaults to preview')
    complete.add_argument('--refresh', action='store_true', help='Invalidate gates after an inspected candidate/base change')
    for name in ('report-pr', 'extend-pr-budget', 'abandon-pr', 'retry-pr-repair', 'recover-pr', 'restart-pr-repair'):
        command = sub.add_parser(name)
        command.add_argument('--config', type=Path, required=True)
        command.add_argument('--pr', type=int, required=True)
        if name != 'report-pr':
            command.add_argument('--reason', required=True)
        else:
            command.add_argument('--markdown', action='store_true')
        if name == 'extend-pr-budget':
            command.add_argument('--calls', type=int, default=0)
            command.add_argument('--seconds', type=int, default=0)
        if name in {'abandon-pr', 'recover-pr'}:
            command.add_argument('--processes-stopped', action='store_true',
                                 help='Confirm all orphaned provider processes were inspected and stopped')
    confirm = sub.add_parser('confirm-validation', help='Record inspection of an exact committed validation plan')
    confirm.add_argument('--config', type=Path, required=True)
    target = confirm.add_mutually_exclusive_group(required=True)
    target.add_argument('--issue', type=int)
    target.add_argument('--pr', type=int)
    confirm.add_argument('--head', required=True)
    confirm.add_argument('--plan-hash', required=True)
    confirm.add_argument('--reason', required=True)
    confirm.add_argument('--batch')
    run = sub.add_parser('run', help='Run the Langflow workflow (preview by default)')
    run.add_argument('--config', type=Path, required=True)
    run.add_argument('--limit', type=int, default=1)
    run.add_argument('--execute', action='store_true', help='Run to the configured PR-ready or merge target')
    status = sub.add_parser('status', help='Read the private execution journal')
    status.add_argument('--config', type=Path, required=True)
    doctor = sub.add_parser('doctor', help='Read-only readiness checks; no coding model or remote mutations')
    doctor.add_argument('--config', type=Path, required=True)
    freeze = sub.add_parser('freeze', help='Freeze an operator-authored task manifest and explicit guidance')
    freeze.add_argument('--config', type=Path, required=True)
    freeze.add_argument('--issue', type=int, required=True)
    freeze.add_argument('--manifest', type=Path, required=True)
    freeze.add_argument('--refresh', action='store_true', help='Explicitly refresh inputs and invalidate prior evidence')
    for name, help_text in (
        ('stop-after-current', 'Finish the admitted ticket, then stop even a preloaded queue'),
        ('resume-queue', 'Explicitly allow admission of another configured ticket'),
    ):
        control = sub.add_parser(name, help=help_text)
        control.add_argument('--config', type=Path, required=True)
    revalidate = sub.add_parser('revalidate', help='Merge current main into the active worktree and invalidate prior gates')
    revalidate.add_argument('--config', type=Path, required=True)
    revalidate.add_argument('--issue', type=int, required=True)
    repair = sub.add_parser('repair-validation', help='Request bounded code repair after inspecting an environment-classified validation failure')
    repair.add_argument('--config', type=Path, required=True)
    target = repair.add_mutually_exclusive_group(required=True)
    target.add_argument('--issue', type=int)
    target.add_argument('--pr', type=int)
    repair.add_argument('--head', required=True, help='Full commit of the inspected failed validation')
    repair.add_argument('--reason', required=True)
    repair.add_argument('--batch', help='Required for the current child of an owned batch')
    abandon = sub.add_parser('abandon-issue', help='Dispose an inspected merged ticket whose documentation verification failed')
    abandon.add_argument('--config', type=Path, required=True)
    abandon.add_argument('--issue', type=int, required=True)
    abandon.add_argument('--reason', required=True)
    abandon.add_argument('--processes-stopped', action='store_true',
                         help='Confirm unresolved provider processes were inspected and stopped')
    retire = sub.add_parser('retire-issue', help='Retire an unfinished ticket whose issue was closed outside this runner')
    retire.add_argument('--config', type=Path, required=True)
    retire.add_argument('--issue', type=int, required=True)
    retire.add_argument('--reason', required=True)
    retire.add_argument('--processes-stopped', action='store_true',
                        help='Confirm unresolved provider processes were inspected and stopped')
    for name in ('extend-budget', 'report', 'note'):
        control = sub.add_parser(name)
        control.add_argument('--config', type=Path, required=True)
        control.add_argument('--issue', type=int, required=True)
        if name == 'report':
            control.add_argument('--json', action='store_true')
            control.add_argument('--markdown', action='store_true')
        else:
            control.add_argument('--reason', required=True)
            if name == 'extend-budget':
                control.add_argument('--calls', type=int, default=0)
                control.add_argument('--seconds', type=int, default=0)
            else:
                control.add_argument('--minutes', type=float, required=True)
                control.add_argument('--category', choices=('intervention', 'review', 'preparation'), required=True)
    setup = sub.add_parser('setup', help='Create private local configuration and Langflow login settings')
    setup.add_argument('--checkout', type=Path, required=True)
    setup.add_argument('--directory', type=Path, required=True)
    serve = sub.add_parser('serve', help='Start the local Langflow UI on loopback')
    serve.add_argument('--directory', type=Path, required=True)
    serve.add_argument('--port', type=int, default=7860)
    for name in ('handoff', 'history', 'validation-preview', 'retention'):
        command = sub.add_parser(name)
        command.add_argument('--config', type=Path, required=True)
        if name == 'handoff':
            target = command.add_mutually_exclusive_group()
            target.add_argument('--issue', type=int)
            target.add_argument('--pr', type=int)
            command.add_argument('--batch')
            command.add_argument('--markdown', action='store_true')
        elif name == 'history':
            command.add_argument('--root', type=Path)
            command.add_argument('--include-siblings', action='store_true')
        elif name == 'validation-preview':
            command.add_argument('--manifest', type=Path)
            command.add_argument('--base')
            command.add_argument('--head')
        else:
            command.add_argument('--apply', action='store_true')
            command.add_argument('--plan-hash')
    args = parser.parse_args()
    if args.command == 'export':
        from .flows import export_flow, build_pr_flow, build_batch_flow, build_analysis_flow
        if sum((args.pr, args.batch, args.analysis)) > 1:
            parser.error('Choose one workflow export')
        if args.pr or args.batch or args.analysis:
            args.path.parent.mkdir(parents=True, exist_ok=True)
            build = build_batch_flow if args.batch else build_pr_flow if args.pr else build_analysis_flow
            args.path.write_text(json.dumps(build(), indent=2) + '\n')
        else:
            export_flow(args.path)
    elif args.command == 'analyze':
        from .flows import run_analysis_flow
        comments = args.comments_file.read_text() if args.comments_file else ''
        if args.refine and not comments.strip():
            parser.error('--refine requires --comments-file with at least one comment')
        if not args.refine and (comments.strip() or args.refine_from != 'research'):
            parser.error('--comments-file and --from apply to --refine only')
        result = run_async(run_analysis_flow(args.config, wish=args.wish, links='\n'.join(args.link), comments=comments,
                                             previous_run_id=args.refine, refine_from=args.refine_from))
        from .flows import document_text
        print(document_text(result))
    elif args.command in {'handoff', 'history', 'validation-preview', 'retention'}:
        from .config import Config, GateError
        from . import operations
        config = Config.load(args.config)
        if args.command == 'handoff':
            result = operations.handoff(config, issue=args.issue, pr=args.pr, batch=args.batch)
            if args.markdown:
                print(operations.markdown(result))
                return
        elif args.command == 'history':
            result = operations.measurements(args.root or config.state_dir, include_siblings=args.include_siblings)
        elif args.command == 'validation-preview':
            manifest = json.loads(args.manifest.read_text()) if args.manifest else None
            selected = None
            if args.base:
                if not args.head:
                    parser.error('--base requires --head with a full commit SHA')
                from .validation import changed_paths, plan, candidate_scope
                try:
                    paths = changed_paths(args.base, args.head, cwd=config.checkout)
                    selected = plan(paths, full=config.validation_mode != 'contextual',
                                    **candidate_scope(paths, args.base, args.head, cwd=config.checkout))
                except ValueError as exc:
                    parser.error(str(exc))
            elif manifest:
                paths = manifest['scope_paths']
            else:
                raise GateError('Supply a manifest or committed base/head for validation preview')
            result = operations.validation_preview(config, paths, manifest=manifest, selected=selected)
        else:
            from .retention import reclaim
            result = reclaim(config, apply=args.apply, plan_hash=args.plan_hash)
        print(json.dumps(result, indent=2))
    elif args.command in {'prepare-batch', 'run-batch', 'start-batch', 'status-batch', 'stop-batch', 'abandon-batch', 'recover-batch', 'extend-batch-budget', '_batch-worker'}:
        from .batches import BatchRunner
        from .config import Config
        runner = BatchRunner(Config.load(args.config), config_path=args.config)
        if args.command == 'prepare-batch':
            result = run_async(runner.prepare(args.spec))
        elif args.command == 'run-batch':
            from .flows import run_batch_flow
            result = run_async(run_batch_flow(args.config, name=args.batch, execute=args.execute, resume=args.resume))
            result = [item.model_dump(mode='json') for item in result]
        elif args.command == 'start-batch':
            from .batch_worker import start
            result = start(args.config, args.batch, resume=args.resume)
        elif args.command == '_batch-worker':
            from .batch_worker import work
            raise SystemExit(work(args.config, args.batch, args.lock_fd, args.token, args.ready_fd, resume=args.resume))
        elif args.command == 'status-batch':
            result = runner.status(args.batch)
        elif args.command == 'stop-batch':
            result = runner.stop(args.batch)
        elif args.command == 'recover-batch':
            result = runner.recover(args.batch, args.reason, processes_stopped=args.processes_stopped)
        elif args.command == 'extend-batch-budget':
            result = runner.extend_budget(args.batch, calls=args.calls, seconds=args.seconds, reason=args.reason)
        else:
            result = runner.abandon(args.batch, args.reason)
        print(json.dumps(result, indent=2))
    elif args.command == 'confirm-validation':
        from .config import Config
        from .validation_review import confirm
        print(json.dumps(run_async(confirm(Config.load(args.config), args.pr or args.issue,
            head=args.head, plan_hash=args.plan_hash, reason=args.reason, batch=args.batch,
            pull_request=args.pr is not None)), indent=2))
    elif args.command == 'complete-pr':
        from .flows import run_pr_flow
        result = run_async(run_pr_flow(args.config, number=args.pr, head=args.head,
                                      execute=args.execute, refresh=args.refresh))
        print(json.dumps([item.model_dump(mode='json') for item in result], indent=2))
    elif args.command in {'report-pr', 'extend-pr-budget', 'abandon-pr', 'retry-pr-repair', 'recover-pr', 'restart-pr-repair'}:
        from .config import Config
        from .pull_requests import PullRequestRunner
        runner = PullRequestRunner(Config.load(args.config))
        if args.command == 'report-pr':
            result = runner.report_pr(args.pr)
            if args.markdown:
                from .operations import handoff, markdown
                print(markdown(handoff(runner.config, pr=args.pr)))
                return
        elif args.command == 'extend-pr-budget':
            result = runner.extend_pr_budget(args.pr, calls=args.calls, seconds=args.seconds, reason=args.reason)
        elif args.command == 'abandon-pr':
            result = run_async(runner.abandon_pr(args.pr, reason=args.reason,
                                               processes_stopped=args.processes_stopped))
        elif args.command == 'recover-pr':
            result = run_async(runner.recover_pr(args.pr, reason=args.reason,
                                               processes_stopped=args.processes_stopped))
        elif args.command == 'restart-pr-repair':
            result = run_async(runner.restart_pr_repair(args.pr, reason=args.reason))
        else:
            result = runner.retry_pr_repair(args.pr, reason=args.reason)
        print(json.dumps(result, indent=2))
    elif args.command in {'setup', 'serve'}:
        from . import local
        if args.command == 'setup':
            local.setup(args.checkout, args.directory)
        else:
            local.serve(args.directory, args.port)
    elif args.command in {'revalidate', 'doctor', 'freeze', 'extend-budget', 'report', 'note', 'abandon-issue',
                          'retire-issue'}:
        from .config import Config
        from .engine import Runner
        runner = Runner(Config.load(args.config))
        if args.command == 'abandon-issue':
            print(json.dumps(run_async(runner.abandon_issue(args.issue, reason=args.reason,
                processes_stopped=args.processes_stopped)), indent=2))
        elif args.command == 'retire-issue':
            print(json.dumps(run_async(runner.retire_issue(args.issue, reason=args.reason,
                processes_stopped=args.processes_stopped)), indent=2))
        elif args.command == 'report':
            report = runner.report(args.issue)
            if args.markdown:
                from .operations import handoff, markdown
                print(markdown(handoff(runner.config, issue=args.issue)))
            elif args.json:
                print(json.dumps(report, indent=2))
            else:
                from .telemetry import format_report
                print(format_report(report))
        elif args.command == 'note':
            print(json.dumps(runner.note(args.issue, minutes=args.minutes, category=args.category, reason=args.reason), indent=2))
        elif args.command == 'extend-budget':
            print(json.dumps(run_async(runner.extend_budget(args.issue, calls=args.calls, seconds=args.seconds, reason=args.reason)), indent=2))
        elif args.command == 'doctor':
            print(json.dumps(run_async(runner.doctor()), indent=2))
        elif args.command == 'freeze':
            print(json.dumps(run_async(runner.freeze(args.issue, args.manifest, refresh=args.refresh)), indent=2))
        else:
            run_async(runner.revalidate(args.issue))
    elif args.command == 'repair-validation':
        from .config import Config
        from .engine import Runner
        from .pull_requests import PullRequestRunner
        is_pr = args.pr is not None
        config = Config.load(args.config)
        number = args.pr if is_pr else args.issue
        kwargs = dict(head=args.head, reason=args.reason, pull_request=is_pr)
        if args.batch:
            from .batches import BatchRunner
            runner = BatchRunner(config, config_path=args.config)
            result = run_async(runner.request_validation_repair(args.batch, number, **kwargs))
        else:
            runner = (PullRequestRunner if is_pr else Runner)(config)
            result = run_async(runner.request_validation_repair(number, **kwargs))
        print(json.dumps(result, indent=2))
    elif args.command in {'status', 'stop-after-current', 'resume-queue'}:
        from .config import Config
        from .state import Store
        config = Config.load(args.config)
        store = Store(config.state_dir)
        if args.command == 'stop-after-current':
            store.stop_after_current()
        elif args.command == 'resume-queue':
            store.resume_queue()
        print(json.dumps({'completion_target': config.completion_target,
                         'control': store.read('control'), 'active': store.read('active'),
                         'last_error': store.read('last_error'), 'tickets': [
            store.read(f'issue-{n}') for n in config.issues if store.read(f'issue-{n}')
        ], 'pr_completions': [store.read(p.stem) for p in sorted(config.state_dir.glob('completion-pr-*.json'))]}, indent=2))
    else:
        from .flows import run_flow
        result = run_async(run_flow(args.config, execute=args.execute, limit=args.limit))
        print(json.dumps([item.model_dump(mode='json') for item in result], indent=2))


if __name__ == '__main__':
    main()
