"""Feature analysis workflow: one request in, one reviewable Markdown document out.

Seven stages, one Langflow node each. A graph edge carries only the run id; every stage
reloads the run from its private journal under the node's own configuration and verifies
the digest chain of what the previous stage consumed. Agents are read-only; the runner
writes the document. The trust boundary follows the ticket runner: repository content, the
request, the links, the comments and fetched web pages are task data, never instructions.
"""

from contextlib import contextmanager
from dataclasses import dataclass
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import time

from .config import GateError
from .evidence import private_bytes, safe_artifact
from . import contracts


STAGES = ('intake', 'research', 'analysis', 'specification', 'review', 'revision', 'document')
RUN_ID = re.compile(r'[0-9a-f]{32}')
SLUG = re.compile(r'[a-z0-9][a-z0-9-]{2,39}')
COMMIT = re.compile(r'[0-9a-f]{40}')


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


@dataclass(frozen=True)
class RunRef:
    """Identity of one analysis run: which checkout, which base commit, which private
    directory, and which stages it inherits from a parent run (refinements only)."""
    run_id: str
    checkout: Path
    state_dir: Path
    base: str
    revision: int = 1
    parent: str | None = None
    inherited: tuple[str, ...] = ()
    slug: str = ''

    @property
    def directory(self) -> Path:
        return run_directory(self.state_dir, self.run_id)

    def owner_id(self, stage: str) -> str:
        if stage not in STAGES:
            raise GateError(f'unknown stage: {stage}')
        return self.parent if stage in self.inherited else self.run_id

    def stage_directory(self, stage: str) -> Path:
        return run_directory(self.state_dir, self.owner_id(stage))


def run_directory(state_dir: Path, run_id) -> Path:
    if not isinstance(run_id, str) or not RUN_ID.fullmatch(run_id):
        raise GateError('Invalid analysis run id')
    return safe_artifact(state_dir, Path('analysis') / run_id)


def numbered_comments(text: str) -> list[dict]:
    """Owner comments, split on blank lines and numbered by the runner, never by the agent."""
    comments = []
    for block in re.split(r'\n\s*\n', text or ''):
        body = block.strip()
        if body:
            comments.append({'id': f'C-{len(comments) + 1}', 'text': body})
    return comments


def new_run(config, *, base: str, wish: str, links: list[str], comments: str,
            parent: RunRef | None = None, refine_from: str | None = None) -> RunRef:
    if not COMMIT.fullmatch(base):
        raise GateError('Invalid base commit')
    inherited, revision, parent_id = (), 1, None
    if parent is not None:
        if refine_from not in STAGES[:4]:
            raise GateError('refine_from must be intake, research, analysis or specification')
        inherited = STAGES[:STAGES.index(refine_from)]
        revision, parent_id = parent.revision + 1, parent.run_id
    run = RunRef(secrets.token_hex(16), config.checkout, config.state_dir, base,
                 revision=revision, parent=parent_id, inherited=inherited)
    record = {'run_id': run.run_id, 'revision': revision, 'parent': parent_id,
              'checkout': str(config.checkout), 'state_dir': str(config.state_dir), 'base': base,
              'created_at': time.time(), 'wish': wish, 'links': list(links),
              'comments': numbered_comments(comments), 'inherited': list(inherited), 'slug': '',
              'calls': [], 'document': None, 'context_hash': None}
    private_bytes(run.directory / 'run.json', json.dumps(record, indent=2, sort_keys=True).encode())
    return run


def read_run(run: RunRef) -> dict:
    path = run.directory / 'run.json'
    if path.is_symlink() or not path.is_file():
        raise GateError(f'analysis run {run.run_id} has no run.json under this state directory')
    try:
        record = json.loads(path.read_bytes())
    except ValueError as exc:
        raise GateError(f'analysis run {run.run_id} record is unreadable') from exc
    if not isinstance(record, dict) or record.get('run_id') != run.run_id:
        raise GateError(f'analysis run {run.run_id} record belongs to another run')
    return record


def update_run(run: RunRef, **fields) -> dict:
    record = read_run(run)
    record.update(fields)
    private_bytes(run.directory / 'run.json', json.dumps(record, indent=2, sort_keys=True).encode())
    return record


def load_run(config, run_id) -> RunRef:
    """Reload a run from its record under *this* configuration's state directory."""
    probe = RunRef(run_id if isinstance(run_id, str) and RUN_ID.fullmatch(run_id) else '', config.checkout,
                   config.state_dir, '')
    if not probe.run_id:
        raise GateError('Invalid analysis run id')
    record = read_run(probe)
    try:
        run = RunRef(record['run_id'], Path(record['checkout']), Path(record['state_dir']), record['base'],
                     revision=record['revision'], parent=record['parent'], inherited=tuple(record['inherited']),
                     slug=record['slug'])
    except (KeyError, TypeError) as exc:
        raise GateError(f'analysis run {run_id} record is malformed') from exc
    if run.checkout != config.checkout or run.state_dir != config.state_dir:
        raise GateError(f'analysis run {run_id} was started for a different checkout or state directory')
    if (not COMMIT.fullmatch(run.base) or type(run.revision) is not int or run.revision < 1
            or (run.parent is not None and not RUN_ID.fullmatch(run.parent))
            or any(s not in STAGES for s in run.inherited) or (run.slug and not SLUG.fullmatch(run.slug))):
        raise GateError(f'analysis run {run_id} record is malformed')
    return run


def journal_path(run: RunRef, stage: str) -> Path:
    return run.stage_directory(stage) / f'{stage}.json'


def record(run: RunRef, stage: str, data: dict, markdown: str, notes, consumed: dict) -> Path:
    """Persist a stage output privately and invalidate every later stage of this run.

    ``consumed`` maps a stage name to ``{'run_id', 'sha256'}`` (or to a bare digest for a
    journal of this run), so a refinement can record exactly which parent journal it read."""
    if stage in run.inherited:
        raise GateError(f'stage {stage} is inherited from the parent run and cannot be recorded here')
    path = journal_path(run, stage)
    for later in STAGES[STAGES.index(stage) + 1:]:
        for stale in (run.directory / f'{later}.json', run.directory / f'{later}.md'):
            if stale.is_symlink() or stale.exists():
                stale.unlink()
    consumed = {name: (value if isinstance(value, dict) else {'run_id': run.owner_id(name), 'sha256': value})
                for name, value in consumed.items()}
    entry = {'stage': stage, 'run': {'run_id': run.run_id, 'checkout': str(run.checkout), 'base': run.base,
                                     'revision': run.revision, 'parent': run.parent},
             'data': data, 'notes': list(notes), 'consumed': consumed,
             'markdown_sha256': digest(markdown.encode()), 'created_at': time.time()}
    private_bytes(path, json.dumps(entry, indent=2, sort_keys=True).encode())
    private_bytes(path.with_suffix('.md'), markdown.encode())
    return path


def recall(run: RunRef, stage: str) -> dict:
    """Load a recorded stage or refuse. The entry's identity must be the owning run's, its
    Markdown twin must match, and every journal it consumed must still be the one on disk."""
    path = journal_path(run, stage)
    owner = run.owner_id(stage)
    if path.is_symlink() or not path.is_file():
        raise GateError(f'stage {stage} has not produced output for run {owner}')
    raw = path.read_bytes()
    try:
        entry = json.loads(raw)
    except ValueError as exc:
        raise GateError(f'stage {stage} journal is unreadable') from exc
    identity = entry.get('run') if isinstance(entry, dict) else None
    if (not isinstance(identity, dict) or entry.get('stage') != stage or identity.get('run_id') != owner
            or identity.get('base') != run.base or identity.get('checkout') != str(run.checkout)):
        raise GateError(f'stage {stage} journal does not belong to this run')
    md = path.with_suffix('.md')
    if md.is_symlink() or not md.is_file() or digest(md.read_bytes()) != entry.get('markdown_sha256'):
        raise GateError(f'stage {stage} report does not match its journal')
    for earlier, expected in (entry.get('consumed') or {}).items():
        if not isinstance(expected, dict) or earlier not in STAGES:
            raise GateError(f'stage {stage} journal is malformed')
        earlier_path = run_directory(run.state_dir, expected.get('run_id')) / f'{earlier}.json'
        if (earlier_path.is_symlink() or not earlier_path.is_file()
                or digest(earlier_path.read_bytes()) != expected.get('sha256')):
            raise GateError(f'stage {stage} consumed a {earlier} journal that has since changed; re-run from {earlier}')
    entry['sha256'] = digest(raw)
    entry['owner'] = owner
    return entry


@contextmanager
def run_lock(run: RunRef):
    run.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(run.directory / 'run.lock', os.O_RDWR | os.O_CREAT, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise GateError('Another analysis stage is active for this run') from exc
        yield
    finally:
        os.close(fd)


async def run_command(commands, args, *, cwd, check=True, timeout=300):
    code, output = await commands.run(tuple(args), cwd=cwd, check=check, timeout=timeout)
    return code, output.rstrip('\n')


async def check_remote(commands, config) -> None:
    _, remote = await run_command(commands, ('git', 'remote', 'get-url', 'origin'), cwd=config.checkout)
    repo = config.repository
    if remote.removesuffix('.git') not in {f'https://github.com/{repo}', f'git@github.com:{repo}'}:
        raise GateError('origin does not match the trusted repository configuration')


async def resolve_base(commands, config) -> str:
    """The commit the analysis reads: origin/main after a fetch, as the ticket runner's freeze does."""
    await check_remote(commands, config)
    await run_command(commands, ('git', 'fetch', 'origin', 'main'), cwd=config.checkout)
    _, base = await run_command(commands, ('git', 'rev-parse', 'origin/main'), cwd=config.checkout)
    if not COMMIT.fullmatch(base):
        raise GateError('origin/main does not resolve to a commit')
    return base


async def ensure_worktree(commands, run: RunRef) -> Path:
    """``<run dir>/worktree`` detached at the run's base commit, created or reused."""
    path = run.directory / 'worktree'
    if path.is_dir():
        code, head = await run_command(commands, ('git', 'rev-parse', 'HEAD'), cwd=path, check=False)
        if code == 0 and head == run.base:
            return path
        raise GateError('analysis worktree exists at a different commit; remove it before continuing')
    run.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    await run_command(commands, ('git', 'worktree', 'add', '--detach', str(path), run.base), cwd=run.checkout)
    return path


async def remove_worktree(commands, run: RunRef) -> None:
    path = run.directory / 'worktree'
    if path.is_dir():
        await run_command(commands, ('git', 'worktree', 'remove', '--force', str(path)), cwd=run.checkout, check=False)


async def guidance_at_base(commands, config, base: str) -> list[dict]:
    """The guidance snapshot the agent sees: tracked documents at the base commit, AGENTS.md
    from the checkout as operator-local guidance. No working-tree comparison."""
    documents = []
    for ref in config.analysis_context_refs:
        contracts.safe_path(ref)
        code, content = await run_command(commands, ('git', 'show', f'{base}:{ref}'), cwd=config.checkout, check=False)
        if code == 0:
            documents.append({'path': ref, 'origin': 'git', 'content': content + '\n'})
            continue
        if ref != 'AGENTS.md':
            raise GateError(f'Missing guidance at the base commit: {ref}')
        path = safe_artifact(config.checkout, ref)
        if not path.is_file() or path.stat().st_size > contracts.MAX_CONTEXT_BYTES:
            raise GateError('Missing or oversized operator-local AGENTS.md')
        documents.append({'path': ref, 'origin': 'operator-local', 'content': path.read_text()})
    contracts.context_hash(documents)
    return documents


def inside_checkout(worktree: Path, path: str) -> bool:
    """True for a relative, dot-dot-free, non-git path to something that exists in the
    worktree without passing through a symlink."""
    if not path or path.startswith('/') or '\x00' in path or '\\' in path:
        return False
    parts = path.split('/')
    if '..' in parts or '.git' in parts or '' in parts:
        return False
    current = worktree
    for part in parts:
        current = current / part
        if current.is_symlink():
            return False
    return current.exists()


def cited_line(worktree: Path, file: str, line) -> str | None:
    """The text of the cited line, or None when the citation does not resolve."""
    if type(line) is not int or line < 1 or not inside_checkout(worktree, file) or not (worktree / file).is_file():
        return None
    try:
        with (worktree / file).open('rb') as handle:
            for number, text in enumerate(handle, 1):
                if number == line:
                    return text.decode('utf-8', 'replace').rstrip('\n')
    except OSError:
        return None
    return None
