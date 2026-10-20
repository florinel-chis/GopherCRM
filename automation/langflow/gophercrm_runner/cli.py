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
    complete = sub.add_parser('complete-pr', help='Review, validate and merge one explicitly pinned existing PR')
    complete.add_argument('--config', type=Path, required=True)
    complete.add_argument('--pr', type=int, required=True)
    complete.add_argument('--head', required=True, help='Full current PR head SHA')
    complete.add_argument('--execute', action='store_true', help='Run to merge; defaults to preview')
    complete.add_argument('--refresh', action='store_true', help='Invalidate gates after an inspected candidate/base change')
    for name in ('report-pr', 'extend-pr-budget'):
        command = sub.add_parser(name)
        command.add_argument('--config', type=Path, required=True)
        command.add_argument('--pr', type=int, required=True)
        if name == 'extend-pr-budget':
            command.add_argument('--calls', type=int, default=0)
            command.add_argument('--seconds', type=int, default=0)
            command.add_argument('--reason', required=True)
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
    for name in ('extend-budget', 'report', 'note'):
        control = sub.add_parser(name)
        control.add_argument('--config', type=Path, required=True)
        control.add_argument('--issue', type=int, required=True)
        if name == 'report':
            control.add_argument('--json', action='store_true')
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
    args = parser.parse_args()
    if args.command == 'export':
        from .flows import export_flow, build_pr_flow
        if args.pr:
            args.path.parent.mkdir(parents=True, exist_ok=True)
            args.path.write_text(json.dumps(build_pr_flow(), indent=2) + '\n')
        else:
            export_flow(args.path)
    elif args.command == 'complete-pr':
        from .flows import run_pr_flow
        result = run_async(run_pr_flow(args.config, number=args.pr, head=args.head,
                                      execute=args.execute, refresh=args.refresh))
        print(json.dumps([item.model_dump(mode='json') for item in result], indent=2))
    elif args.command in {'report-pr', 'extend-pr-budget'}:
        from .config import Config
        from .pull_requests import PullRequestRunner
        runner = PullRequestRunner(Config.load(args.config))
        result = runner.report_pr(args.pr) if args.command == 'report-pr' else runner.extend_pr_budget(
            args.pr, calls=args.calls, seconds=args.seconds, reason=args.reason)
        print(json.dumps(result, indent=2))
    elif args.command in {'setup', 'serve'}:
        from . import local
        if args.command == 'setup':
            local.setup(args.checkout, args.directory)
        else:
            local.serve(args.directory, args.port)
    elif args.command in {'revalidate', 'doctor', 'freeze', 'extend-budget', 'report', 'note'}:
        from .config import Config
        from .engine import Runner
        runner = Runner(Config.load(args.config))
        if args.command == 'report':
            report = runner.report(args.issue)
            if args.json:
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
