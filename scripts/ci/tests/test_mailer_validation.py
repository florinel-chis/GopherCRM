import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'automation/langflow'))
sys.path.insert(0, str(ROOT / 'scripts/ci'))
from gophercrm_runner import validation as v
import go_checks

spec = importlib.util.spec_from_file_location('mailer_profiles', ROOT / 'scripts/ci/check-go.py')
profiles = importlib.util.module_from_spec(spec)
spec.loader.exec_module(profiles)


class MailerValidationTests(unittest.TestCase):
    def test_only_mailer_scope_uses_transport_callers_and_health(self):
        paths = ['internal/mailer/smtp.go', 'internal/mailer/smtp_test.go', 'README.md']
        selected = v.plan(paths, mailer_roots=True)
        self.assertEqual(selected['profile'], 'mailer')
        self.assertEqual(selected['specs'], [v.SMOKE])
        self.assertEqual(v.core_commands([('make', 'verify'), ('custom',)], selected), [
            ('make', 'verify-hygiene'), ('python3', 'scripts/ci/check-go.py', 'mailer'),
            ('npm', 'ci', '--prefix', 'gocrm-ui'), ('custom',)])
        for name in v.MAILER_FILES:
            self.assertEqual(v.plan([name], mailer_roots=True)['profile'], 'mailer')
        for extra in ['internal/mailer/new_transport.go', 'internal/mailer/mailer.go', 'internal/mailer/log.go', 'internal/service/auth_service.go',
                      'internal/service/form_service.go', 'internal/service/form_service_test.go',
                      'internal/repository/user_repository.go', 'internal/config/config.go',
                      'internal/models/user.go', 'internal/utils/log_error.go', 'cmd/main.go',
                      'migrations/022.sql', 'go.mod', 'gocrm-ui/src/api/client.ts',
                      'internal/aeo/engine.go', 'internal/utils/transaction.go', 'unknown.go']:
            with self.subTest(extra=extra):
                self.assertEqual(v.plan(paths + [extra], aeo_roots=True, mailer_roots=True)['profile'], 'full')
        mixed = v.plan(paths + ['scripts/ci/check-go.py'], mailer_roots=True)
        self.assertEqual(mixed['profile'], 'mailer')
        self.assertIn(('make', 'verify-runner'), v.core_commands([], mixed))
        self.assertEqual(v.plan(paths)['profile'], 'full')
        full = v.plan(paths, full=True, mailer_roots=True)
        self.assertEqual(full['profile'], 'full')
        self.assertEqual(full['specs'], [])
        self.assertEqual(v.core_commands([], full, explicit_full=True), [
            ('make', 'verify'), ('python3', 'scripts/ci/check-go.py', 'transaction')])

    def test_real_candidate_with_documentation_comments_and_active_setting_rejection(self):
        # Frozen minimal fixtures describe the intended edit shape without
        # depending on mutable production source or claiming acceptance passes.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root,
                    stderr=subprocess.DEVNULL, text=True).strip()
            git('init'); git('config', 'user.email', 'test@example.test'); git('config', 'user.name', 'Test')
            source = root / 'internal/mailer/smtp.go'
            source.parent.mkdir(parents=True)
            source.write_text('package mailer\n')
            env = root / '.env.example'
            env.write_text('# SMTP settings\nDB_HOST=localhost\n')
            git('add', '.'); git('commit', '-m', 'base'); base = git('rev-parse', 'HEAD')
            source.write_text('package mailer\n// Bounded SMTP transport.\n')
            env.write_text('# SMTP operations have a deadline\nDB_HOST=localhost\n')
            (root / 'README.md').write_text('SMTP operations are bounded.\n')
            roots = root / 'internal/mailer/smtp_test.go'
            roots.write_text('package mailer\nimport "testing"\n' + ''.join(
                f'func {name}(t *testing.T) {{}}\n' for name in sorted(v.MAILER_REQUIRED_ROOTS)))
            git('add', '.'); git('commit', '-m', 'candidate'); head = git('rev-parse', 'HEAD')
            paths = v.changed_paths(base, head, cwd=root)
            selected = v.plan(paths, **v.candidate_scope(paths, base, head, cwd=root))
            self.assertEqual(selected['profile'], 'mailer')
            self.assertEqual(selected['specs'], [v.SMOKE])
            self.assertFalse(v.mailer_roots_present(base, cwd=root))
            self.assertFalse(v.mailer_roots_present('HEAD', cwd=root))
            complete = roots.read_text()
            for name in sorted(v.MAILER_REQUIRED_ROOTS):
                roots.write_text(complete.replace(f'func {name}(t *testing.T) {{}}\n', ''))
                git('add', '.'); git('commit', '-m', 'missing root')
                missing = git('rev-parse', 'HEAD')
                self.assertEqual(v.plan(paths, **v.candidate_scope(paths, base, missing, cwd=root))['profile'], 'full')
                # A dirty worktree cannot supply proof for a committed candidate.
                roots.write_text(complete)
                self.assertFalse(v.mailer_roots_present(missing, cwd=root))
            roots.write_text(complete + complete.splitlines()[-1] + '\n')
            git('add', '.'); git('commit', '-m', 'duplicate root')
            self.assertFalse(v.mailer_roots_present(git('rev-parse', 'HEAD'), cwd=root))
            roots.write_text(complete)
            env.write_text('# SMTP operations have a deadline\nDB_HOST=remote\n')
            git('add', '.'); git('commit', '-m', 'active setting'); head = git('rev-parse', 'HEAD')
            self.assertEqual(v.plan(paths, **v.candidate_scope(paths, base, head, cwd=root))['profile'], 'full')

    def test_mailer_checker_requires_every_boundary_and_caller_before_execution(self):
        discovered = go_checks.discover(profiles.MAILER_SOURCES)
        self.assertTrue(profiles.MAILER_CALLER_ROOTS.issubset(discovered['internal/service']))
        selected = dict(discovered, **{'internal/mailer': sorted(
            set(discovered['internal/mailer']) | v.MAILER_REQUIRED_ROOTS)})
        with patch.dict(os.environ, {}, clear=True), patch.object(go_checks, 'discover', return_value=selected), \
                patch.object(go_checks, 'run') as run:
            profiles.run('mailer')
            actual = run.call_args.args[0]
            self.assertEqual(set(actual['internal/service']), profiles.MAILER_CALLER_ROOTS)
            self.assertEqual(actual['internal/mailer'], selected['internal/mailer'])
            self.assertEqual(actual['internal/utils'], discovered['internal/utils'])
            self.assertNotIn('known_skips', run.call_args.kwargs)
        for package, roots in [('internal/mailer', v.MAILER_REQUIRED_ROOTS),
                               ('internal/service', profiles.MAILER_CALLER_ROOTS)]:
            for name in roots:
                missing = dict(selected, **{package: [x for x in selected[package] if x != name]})
                with self.subTest(name=name), patch.object(go_checks, 'discover', return_value=missing), \
                        patch.object(go_checks, 'run') as run, self.assertRaises(go_checks.GateError):
                    profiles.run('mailer')
                run.assert_not_called()
        with self.assertRaises(go_checks.GateError):
            profiles.run('mailer', storage_only=True)

    def test_smtp_missing_skipped_or_failed_roots_are_not_accepted(self):
        module = 'example/internal/mailer'
        for root in v.MAILER_REQUIRED_ROOTS:
            for action in ['skip', 'fail', None]:
                events = [] if action is None else [{'Package': module, 'Test': root, 'Action': action}]
                events.append({'Package': module, 'Action': 'pass'})
                with self.subTest(root=root, action=action), self.assertRaises(go_checks.GateError):
                    go_checks.verify('\n'.join(json.dumps(e) for e in events), module, [root], {})

    def test_ci_uses_same_check_and_keeps_migration_and_release_gates(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        self.assertIn("if: needs.plan.outputs.profile == 'mailer'\n        run: python3 scripts/ci/check-go.py mailer", workflow)
        self.assertEqual(workflow.count("if: needs.plan.outputs.profile != 'mailer' &&"), 2)
        self.assertIn("if: needs.plan.outputs.profile == 'mailer' || needs.plan.outputs.profile == 'runner'", workflow)
        self.assertIn('uses: ./.github/workflows/migrations.yml', workflow)
        self.assertIn('name: E2E (Playwright on ${{ matrix.db.name }})', workflow)
        self.assertIn('id: mariadb1011', workflow)
        self.assertIn('full: true', (ROOT / '.github/workflows/release.yml').read_text())


if __name__ == '__main__':
    unittest.main()
