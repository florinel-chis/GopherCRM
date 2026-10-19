"""Trusted, local execution policy. Issue text never supplies commands or paths."""

from dataclasses import dataclass
import json
from pathlib import Path
import re


REQUIRED_CHECKS = (
    'Repository hygiene', 'Backend (build, vet, test)', 'Frontend (build, lint, test)',
    'E2E (Playwright on MySQL 8)', 'E2E (Playwright on MariaDB 10.11)',
    'E2E (Playwright on SQLite)',
)


class GateError(RuntimeError):
    """Execution stopped before an unsafe or unverified transition."""


class CommandError(GateError):
    """A completed command failed; its diagnostics can inform a repair."""


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

    @classmethod
    def load(cls, path: str | Path) -> 'Config':
        raw = json.loads(Path(path).read_text())
        for name in ('checkout', 'state_dir'):
            value = Path(raw[name]).expanduser()
            if not value.is_absolute():
                raise GateError(f'{name} must be an absolute path')
            raw[name] = value.resolve()
        for name in ('issues', 'required_checks', 'workflow_addition_issues'):
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
            raise GateError('make verify is a mandatory local gate')
        if not set(REQUIRED_CHECKS).issubset(self.required_checks):
            raise GateError('All six repository checks must remain required')
        for name in ('agent_timeout_seconds', 'test_timeout_seconds', 'ci_timeout_seconds',
                     'ci_poll_seconds', 'max_repair_rounds'):
            if type(getattr(self, name)) is not int or getattr(self, name) <= 0:
                raise GateError(f'{name} must be a positive integer')
        if not (self.checkout / '.git').exists():
            raise GateError('checkout must be an existing Git checkout')
        if self.state_dir == self.checkout or self.state_dir.is_relative_to(self.checkout):
            raise GateError('Keep private state and worktrees outside the source checkout')

    def allows(self, issue: int) -> None:
        if type(issue) is not int or issue not in self.issues:
            raise GateError('Issue is outside the configured remediation queue')
