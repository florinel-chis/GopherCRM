"""CLI that executes the same Langflow graph imported into the visual editor."""

import argparse
import asyncio
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    export = sub.add_parser('export', help='Generate the importable Langflow workflow')
    export.add_argument('path', type=Path)
    run = sub.add_parser('run', help='Run the Langflow workflow (preview by default)')
    run.add_argument('--config', type=Path, required=True)
    run.add_argument('--limit', type=int, default=1)
    run.add_argument('--execute', action='store_true', help='Run to the configured PR-ready or merge target')
    status = sub.add_parser('status', help='Read the private execution journal')
    status.add_argument('--config', type=Path, required=True)
    doctor = sub.add_parser('doctor', help='Read-only readiness checks; no coding model or remote mutations')
    doctor.add_argument('--config', type=Path, required=True)
    for name, help_text in (
        ('stop-after-current', 'Finish the admitted ticket, then stop even a preloaded queue'),
        ('resume-queue', 'Explicitly allow admission of another configured ticket'),
    ):
        control = sub.add_parser(name, help=help_text)
        control.add_argument('--config', type=Path, required=True)
    revalidate = sub.add_parser('revalidate', help='Merge current main into the active worktree and invalidate prior gates')
    revalidate.add_argument('--config', type=Path, required=True)
    revalidate.add_argument('--issue', type=int, required=True)
    setup = sub.add_parser('setup', help='Create private local configuration and Langflow login settings')
    setup.add_argument('--checkout', type=Path, required=True)
    setup.add_argument('--directory', type=Path, required=True)
    serve = sub.add_parser('serve', help='Start the local Langflow UI on loopback')
    serve.add_argument('--directory', type=Path, required=True)
    serve.add_argument('--port', type=int, default=7860)
    args = parser.parse_args()
    if args.command == 'export':
        from .flows import export_flow
        export_flow(args.path)
    elif args.command in {'setup', 'serve'}:
        from . import local
        if args.command == 'setup':
            local.setup(args.checkout, args.directory)
        else:
            local.serve(args.directory, args.port)
    elif args.command in {'revalidate', 'doctor'}:
        from .config import Config
        from .engine import Runner
        runner = Runner(Config.load(args.config))
        if args.command == 'doctor':
            print(json.dumps(asyncio.run(runner.doctor()), indent=2))
        else:
            asyncio.run(runner.revalidate(args.issue))
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
        ]}, indent=2))
    else:
        from .flows import run_flow
        result = asyncio.run(run_flow(args.config, execute=args.execute, limit=args.limit))
        print(json.dumps([item.model_dump(mode='json') for item in result], indent=2))


if __name__ == '__main__':
    main()
