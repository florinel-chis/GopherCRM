"""Trusted, local execution policy. Issue text never supplies commands or paths."""

from dataclasses import dataclass, field
import json
from pathlib import Path
import re


REQUIRED_CHECKS = (
    'Repository hygiene', 'Backend (build, vet, test)', 'Frontend (build, lint, test)',
    'E2E (Playwright on MySQL 8)', 'E2E (Playwright on MariaDB 10.11)',
    'E2E (Playwright on SQLite)',
)
DATABASE_CHECKS = (
    ('make', 'e2e', 'E2E_DB_DRIVER=sqlite'), ('make', 'e2e', 'E2E_DB_DRIVER=mysql'),
    ('go', 'test', '-json', '-count=1', './cmd/migrate'),
)


class GateError(RuntimeError):
    """Execution stopped before an unsafe or unverified transition."""


class CommandError(GateError):
    """A completed command failed; its diagnostics can inform a repair."""

    def __init__(self, message, *, stdout=None, stderr=None):
        super().__init__(message)
        self.stdout = stdout
        self.stderr = stderr


class EnvironmentError(GateError):
    """An operator prerequisite failed; do not ask a coding agent to repair it."""


class BudgetError(GateError):
    """Durable execution limits prohibit another mutating step or provider call."""


CHECK_KINDS = ('go', 'vitest')
# Run from the repository root. `--silent` keeps npm's script banner off stdout,
# so the Vitest JSON report is the command's only standard output.
VITEST_ARGV = ('npm', '--prefix', 'gocrm-ui', 'test', '--silent', '--', '--run', '--reporter=json')
VITEST_FILE = re.compile(r'src/(?:[A-Za-z0-9_.@-]+/)*[A-Za-z0-9_.@-]+\.test\.tsx?')


def check_kind(check):
    """An entry without `kind` is a Go check, so existing registries keep their hashes."""
    return check.get('kind', 'go')


def validate_vitest_check(argv, tests):
    files = argv[len(VITEST_ARGV):] if isinstance(argv, list) else []
    if (not isinstance(argv, list) or tuple(argv[:len(VITEST_ARGV)]) != VITEST_ARGV or not files
            or any(not isinstance(f, str) or not VITEST_FILE.fullmatch(f) or '..' in f.split('/')
                   for f in files) or len(set(files)) != len(files)):
        raise GateError('Vitest acceptance checks require the exact npm JSON reporter argument array '
                        'and unique test files under gocrm-ui/src')
    if (not isinstance(tests, list) or not tests or any(
            not isinstance(t, dict) or set(t) != {'file', 'name'} or t['file'] not in files
            or not isinstance(t['name'], str) or not t['name'].strip() or '\x00' in t['name']
            for t in tests)):
        raise GateError('Vitest acceptance checks require exact file/test identities from their argv files')
    if len({(t['file'], t['name']) for t in tests}) != len(tests):
        raise GateError('Duplicate acceptance test identity')


def validate_acceptance_checks(registry):
    """The trusted check registry: uncached go test -json argument arrays, or the exact Vitest JSON
    reporter command, with exact test identities."""
    if not isinstance(registry, dict):
        raise GateError('acceptance_checks must be a trusted check registry')
    for check_id, check in registry.items():
        if not re.fullmatch(r'[a-z][a-z0-9_-]{0,63}', check_id):
            raise GateError('Invalid acceptance check ID')
        if not isinstance(check, dict) or set(check) - {'kind'} != {'argv', 'tests'}:
            raise GateError('Acceptance checks require argv and named tests')
        if check_kind(check) not in CHECK_KINDS:
            raise GateError('Acceptance check kind must be go or vitest')
        argv, tests = check['argv'], check['tests']
        if check_kind(check) == 'vitest':
            validate_vitest_check(argv, tests)
            continue
        if (not isinstance(argv, list) or any(not isinstance(a, str) or not a or '\x00' in a for a in argv)
                or argv[:2] != ['go', 'test'] or '-json' not in argv or '-count=1' not in argv):
            raise GateError('MVP acceptance checks require uncached go test -json argument arrays')
        if (not isinstance(tests, list) or not tests or any(
                not isinstance(t, dict) or set(t) != {'package', 'name'}
                or not isinstance(t['package'], str) or not t['package']
                or not isinstance(t['name'], str) or not re.fullmatch(r'Test[A-Za-z0-9_]+', t['name'])
                for t in tests)):
            raise GateError('Acceptance checks require exact package/test identities')
        if len({(t['package'], t['name']) for t in tests}) != len(tests):
            raise GateError('Duplicate acceptance test identity')


@dataclass(frozen=True)
class Config:
    repository: str
    checkout: Path
    state_dir: Path
    issues: tuple[int, ...]
    tracker: int = 94
    required_checks: tuple[str, ...] = REQUIRED_CHECKS
    test_commands: tuple[tuple[str, ...], ...] = (('make', 'verify'),)
    database_test_commands: tuple[tuple[str, ...], ...] = ()
    workflow_addition_issues: tuple[int, ...] = ()
    agent_timeout_seconds: int = 1800
    test_timeout_seconds: int = 3600
    ci_timeout_seconds: int = 3600
    ci_poll_seconds: int = 30
    max_repair_rounds: int = 3
    # Omitted in older operator policies: preserve their authorized merge target.
    completion_target: str = 'merge'
    min_free_disk_mb: int = 4096
    disposable_database: bool = False
    chromium_path: str = ''
    acceptance_checks: dict = field(default_factory=dict)
    agent_model: str = ''
    max_provider_calls: int = 8
    issue_timeout_seconds: int = 14400
    # An omitted mode retains v0.3 behavior and evidence hashes. Explicit full
    # runs the complete browser/database matrix even for UI-only changes.
    validation_mode: str = 'legacy'
    # Existing journals keep their original approval schema; new policies opt in.
    documentation_mode: str = 'legacy'
    # Operator inspection of the committed plan, before any local checks.
    validation_review_required: bool = False
    # Feature analysis workflow settings. Never part of the execution policy hash.
    analysis_output_dir: str = 'docs/specs'
    analysis_max_calls: int = 8
    analysis_context_refs: tuple[str, ...] = ('AGENTS.md', 'docs/DEVELOPMENT.md')
    analysis_rules_dir: str = ''

    @classmethod
    def load(cls, path: str | Path) -> 'Config':
        raw = json.loads(Path(path).read_text())
        for name in ('checkout', 'state_dir'):
            value = Path(raw[name]).expanduser()
            if not value.is_absolute():
                raise GateError(f'{name} must be an absolute path')
            raw[name] = value.resolve()
        for name in ('issues', 'required_checks', 'workflow_addition_issues', 'analysis_context_refs'):
            if name in raw:
                raw[name] = tuple(raw[name])
        for name in ('test_commands', 'database_test_commands'):
            if name in raw:
                commands = raw[name]
                if not isinstance(commands, list) or any(
                    not isinstance(c, list) or not c or
                    any(not isinstance(a, str) or not a or '\x00' in a for a in c)
                    for c in commands
                ):
                    raise GateError(f'{name} must contain argument arrays, not shell strings')
                raw[name] = tuple(tuple(c) for c in commands)
        result = cls(**raw)
        result.validate()
        return result

    def validate(self) -> None:
        if type(self.validation_review_required) is not bool:
            raise GateError('validation_review_required must be boolean')
        if self.documentation_mode not in {'legacy', 'reviewed'}:
            raise GateError('documentation_mode must be legacy or reviewed')
        if self.validation_mode not in {'legacy', 'full', 'contextual'}:
            raise GateError('validation_mode must be legacy, full or contextual')
        if self.validation_mode == 'full' and not self.database_test_commands:
            raise GateError('Explicit full validation requires database_test_commands')
        if self.database_test_commands and not set(DATABASE_CHECKS).issubset(self.database_test_commands):
            raise GateError('The database validation matrix requires SQLite, MySQL and uncached JSON migration checks')
        if not isinstance(self.agent_model, str) or '\x00' in self.agent_model:
            raise GateError('agent_model must be a string')
        validate_acceptance_checks(self.acceptance_checks)
        output_dir = self.analysis_output_dir
        if (not isinstance(output_dir, str) or not output_dir or output_dir.startswith('/') or '\x00' in output_dir
                or '..' in output_dir.split('/')):
            raise GateError('analysis_output_dir must be a relative path inside the checkout')
        if type(self.analysis_max_calls) is not int or self.analysis_max_calls <= 0:
            raise GateError('analysis_max_calls must be a positive integer')
        refs = self.analysis_context_refs
        if (not isinstance(refs, tuple) or not refs or 'AGENTS.md' not in refs or len(set(refs)) != len(refs)
                or any(not isinstance(r, str) or not r.endswith('.md') or r.startswith('/') or '..' in r.split('/')
                       or '\x00' in r for r in refs)):
            raise GateError('analysis_context_refs must be unique Markdown paths including AGENTS.md')
        if not isinstance(self.analysis_rules_dir, str) or (self.analysis_rules_dir and not self.analysis_rules_dir.startswith('/')):
            raise GateError('analysis_rules_dir must be empty or an absolute operator-owned directory')
        if type(self.disposable_database) is not bool or not isinstance(self.chromium_path, str):
            raise GateError('disposable_database must be boolean and chromium_path a string')
        if self.completion_target not in {'pr', 'merge'}:
            raise GateError('completion_target must be pr or merge')
        if not re.fullmatch(r'[A-Za-z0-9][\w.-]*/[A-Za-z0-9][\w.-]*', self.repository):
            raise GateError('Invalid GitHub owner/repository')
        if not self.issues or any(type(n) is not int or n <= 0 for n in self.issues):
            raise GateError('issues must be an explicit list of positive issue numbers')
        if len(set(self.issues)) != len(self.issues) or self.tracker in self.issues:
            raise GateError('Duplicate issue or tracking issue in execution queue')
        if any(type(n) is not int or n not in self.issues for n in self.workflow_addition_issues):
            raise GateError('Workflow additions must be authorized for explicit queued issue numbers')
        if ('make', 'verify') not in self.test_commands:
            raise GateError('make verify must remain registered for application and full validation')
        if not set(REQUIRED_CHECKS).issubset(self.required_checks):
            raise GateError('All six repository checks must remain required')
        if type(self.max_repair_rounds) is not int or self.max_repair_rounds < 0:
            raise GateError('max_repair_rounds must be a nonnegative integer')
        for name in ('agent_timeout_seconds', 'test_timeout_seconds', 'ci_timeout_seconds',
                     'ci_poll_seconds', 'min_free_disk_mb',
                     'max_provider_calls', 'issue_timeout_seconds'):
            if type(getattr(self, name)) is not int or getattr(self, name) <= 0:
                raise GateError(f'{name} must be a positive integer')
        if not (self.checkout / '.git').exists():
            raise GateError('checkout must be an existing Git checkout')
        if self.state_dir == self.checkout or self.state_dir.is_relative_to(self.checkout):
            raise GateError('Keep private state and worktrees outside the source checkout')

    def allows(self, issue: int) -> None:
        if type(issue) is not int or issue not in self.issues:
            raise GateError('Issue is outside the configured remediation queue')
