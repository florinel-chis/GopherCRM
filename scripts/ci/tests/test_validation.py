import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "automation/langflow"))
from gophercrm_runner.validation import browser_command, changed_paths, core_commands, plan, SMOKE, POLICY_VERSION


class ValidationSelectionTests(unittest.TestCase):
    def test_feature_changes_select_smoke_and_related_suites(self):
        selected = plan(["internal/repository/ticket_repository.go",
                         "internal/integration/ticket_visibility_integration_test.go"])
        self.assertEqual(selected["profile"], "contextual")
        self.assertEqual(set(selected["specs"]), {SMOKE, "e2e/tests/admin-tickets.spec.ts", "e2e/tests/admin-entity-suite.spec.ts"})
        self.assertEqual(selected, plan(list(reversed(selected["changed_paths"]))))
        self.assertEqual(browser_command(("make", "e2e", "E2E_DB_DRIVER=mysql"), selected),
                         ("make", "e2e", "E2E_DB_DRIVER=mysql",
                          "SPECS=e2e/tests/admin-entity-suite.spec.ts e2e/tests/admin-tickets.spec.ts e2e/tests/health-smoke.spec.ts"))
        self.assertEqual(browser_command(("go", "test", "./cmd/migrate"), selected),
                         ("go", "test", "./cmd/migrate"))

    def test_known_reverse_consumers_and_cross_entity_journeys_are_selected(self):
        cases = {
            "internal/repository/lead_repository.go": ["forms-public-api", "admin-tasks", "admin-entity-suite", "customer-website", "admin-companies"],
            "internal/service/customer_service.go": ["admin-tasks", "admin-entity-suite", "admin-deals"],
            "gocrm-ui/src/pages/deals/DealsSection.tsx": ["admin-customers"],
            "gocrm-ui/src/pages/tasks/TaskList.tsx": ["admin-entity-suite"],
            "gocrm-ui/src/pages/users/UserList.tsx": ["admin-entity-suite"],
        }
        for path, names in cases.items():
            selected = plan([path])
            with self.subTest(path=path):
                self.assertEqual(selected["profile"], "contextual")
                self.assertTrue({f"e2e/tests/{name}.spec.ts" for name in names}.issubset(selected["specs"]))
        self.assertEqual(plan(["internal/repository/user_repository.go"])["profile"], "full")

    def test_related_features_are_unioned_not_last_match_wins(self):
        selected = plan(["gocrm-ui/src/pages/leads/LeadList.tsx",
                         "internal/repository/customer_repository.go"])
        self.assertIn("e2e/tests/leads-convert-api.spec.ts", selected["specs"])
        self.assertIn("e2e/tests/customer-website.spec.ts", selected["specs"])
        self.assertEqual(selected["features"], ["customer", "lead"])

    def test_shared_security_schema_dependencies_and_unknown_paths_go_full(self):
        for path in ["internal/middleware/auth.go",
                     "internal/models/ticket.go", "internal/service/interfaces.go",
                     "internal/handler/routes.go", "migrations/new.sql", "go.mod",
                     "gocrm-ui/package-lock.json", "gocrm-ui/src/components/Table.tsx",
                     "scripts/release/release.py", ".github/workflows/release.yml", "new-feature.bin"]:
            with self.subTest(path=path):
                self.assertEqual(plan([path])["profile"], "full")
                self.assertEqual(plan([path])["specs"], [])
        self.assertEqual(plan([])["profile"], "full")
        self.assertEqual(plan(["README.md"], full=True)["profile"], "full")
        self.assertEqual(plan(["docs/DEVELOPMENT.md"])["specs"], [SMOKE])

    def test_runner_only_uses_relevant_checks_and_smoke_without_full_application_tests(self):
        paths = ['automation/langflow/gophercrm_runner/batches.py', 'scripts/ci/plan-validation.py',
                 '.github/workflows/ci.yml', 'docs/VALIDATION_RELEASE.md']
        selected = plan(paths)
        self.assertEqual(selected['profile'], 'runner')
        self.assertEqual(selected['specs'], [SMOKE])
        self.assertEqual(core_commands([('make', 'verify'), ('make', 'verify-runner')], selected),
                         [('make', 'verify-hygiene'), ('make', 'verify-runner'),
                          ('npm', 'ci', '--prefix', 'gocrm-ui')])
        mixed = plan(paths + ['internal/repository/ticket_repository.go'])
        self.assertEqual(mixed['profile'], 'contextual')
        self.assertIn('e2e/tests/admin-tickets.spec.ts', mixed['specs'])
        self.assertEqual(core_commands([], mixed), [('make', 'verify')])
        full = plan(paths, full=True)
        self.assertEqual(full['profile'], 'full')
        self.assertEqual(full['specs'], [])
        self.assertEqual(core_commands([], full), [('make', 'verify')])
        self.assertEqual(plan(paths + ['internal/middleware/auth.go'])['profile'], 'full')

    def test_auth_checks_are_bounded_and_retain_custom_commands(self):
        paths = ['internal/service/auth_service.go', 'internal/service/auth_lockout_test.go',
                 'tests/auth_session_integration_test.go', 'docs/FEATURES.md']
        selected = plan(paths)
        self.assertEqual(selected['profile'], 'auth')
        self.assertEqual(set(selected['specs']), {SMOKE, 'e2e/tests/login.spec.ts', 'e2e/tests/registration.spec.ts'})
        custom = ('go', 'test', './extra')
        self.assertEqual(core_commands([('make', 'verify'), custom], selected), [
            ('make', 'verify-hygiene'), ('python3', 'scripts/ci/check-auth.py'),
            ('npm', 'ci', '--prefix', 'gocrm-ui'), custom])
        mixed = plan(paths + ['automation/langflow/gophercrm_runner/validation.py'])
        self.assertEqual(mixed['profile'], 'auth')
        self.assertIn(('make', 'verify-runner'), core_commands([], mixed))
        for path in ['internal/service/customer_service.go', 'internal/repository/user_repository.go',
                     'internal/models/user.go', 'internal/service/auth_new.go', 'unknown.go', 'api/swagger.json']:
            with self.subTest(path=path):
                self.assertEqual(plan(paths + [path])['profile'], 'full')
        self.assertEqual(plan(paths, full=True)['profile'], 'full')
        self.assertEqual(plan(['scripts/ci/check-auth.py'])['profile'], 'auth')

    def test_paths_cannot_inject_commands_or_github_outputs(self):
        for path in ["../secret", "/tmp/file", "docs/x\nspecs=evil.md", r"..\file", "x\0y"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                plan([path])
        self.assertEqual(plan(["gocrm-ui/e2e/tests/$(touch pwned).spec.ts"])["profile"], "full")

    def test_transaction_profile_uses_callers_and_health_with_explicit_full_override(self):
        paths = ['internal/utils/transaction.go', 'internal/utils/transaction_recovery_test.go',
                 'automation/langflow/gophercrm_runner/validation.py', 'docs/DEVELOPMENT.md']
        selected = plan(paths)
        self.assertEqual(selected['profile'], 'transaction')
        self.assertEqual(selected['specs'], [SMOKE])
        commands = core_commands([('make', 'verify'), ('go', 'test', './extra')], selected)
        self.assertIn(('python3', 'scripts/ci/check-go.py', 'transaction'), commands)
        self.assertIn(('make', 'verify-runner'), commands)
        self.assertIn(('go', 'test', './extra'), commands)
        self.assertNotIn(('make', 'verify'), commands)
        self.assertEqual(plan(paths, full=True)['profile'], 'full')
        for unrelated in ['internal/utils/transaction_other.go', 'internal/utils/password.go', 'internal/service/form_service.go',
                          'internal/service/auth_service.go', 'go.mod', 'migrations/new.sql']:
            self.assertEqual(plan(paths + [unrelated])['profile'], 'full')

    def test_changed_test_is_included_and_empty_filter_is_never_contextual(self):
        selected = plan(["gocrm-ui/e2e/tests/new-feature.spec.ts"])
        self.assertIn("e2e/tests/new-feature.spec.ts", selected["specs"])
        self.assertIn(SMOKE, selected["specs"])
        with self.assertRaises(ValueError):
            browser_command(("make", "e2e", "SPECS="), selected)

    def test_real_git_diff_keeps_deleted_and_renamed_paths(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root,
                                               stderr=subprocess.DEVNULL, text=True).strip()
            git("init", "-b", "main")
            git("config", "user.email", "test@example.test")
            git("config", "user.name", "Validation Test")
            (root / "old.go").write_text("package old\n")
            git("add", ".")
            git("commit", "-m", "base")
            base = git("rev-parse", "HEAD")
            git("mv", "old.go", "new.go")
            git("commit", "-m", "rename")
            head = git("rev-parse", "HEAD")
            self.assertEqual(changed_paths(base, head, cwd=root), ["new.go", "old.go"])
            with self.assertRaises(ValueError):
                changed_paths("--all", head, cwd=root)
            with self.assertRaises(subprocess.CalledProcessError):
                changed_paths(head, base, cwd=root)

    def test_ci_executes_base_selector_not_candidate_replacement(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, text=True,
                                               stderr=subprocess.DEVNULL).strip()
            git("init", "-b", "main")
            git("config", "user.email", "test@example.test")
            git("config", "user.name", "Validation Test")
            selector = root / "automation/langflow/gophercrm_runner/validation.py"
            selector.parent.mkdir(parents=True)
            selector.write_text((ROOT / "automation/langflow/gophercrm_runner/validation.py").read_text())
            git("add", ".")
            git("commit", "-m", "trusted base")
            base = git("rev-parse", "HEAD")
            selector.write_text('raise RuntimeError("candidate selector executed")\n')
            git("add", ".")
            git("commit", "-m", "replace selector")
            event = root / "event.json"
            event.write_text(json.dumps({"pull_request": {"base": {"sha": base}}}))
            output = root / "output"
            env = dict(os.environ, GITHUB_EVENT_PATH=str(event), GITHUB_EVENT_NAME="pull_request",
                       GITHUB_OUTPUT=str(output), RUNNER_TEMP=folder)
            env.pop("GITHUB_STEP_SUMMARY", None)
            subprocess.run([sys.executable, str(ROOT / "scripts/ci/plan-validation.py")],
                           cwd=root, env=env, check=True, capture_output=True, text=True)
            selected = json.loads((root / "validation-plan.json").read_text())
            # The fixture has no smoke spec, so the trusted selector falls back
            # to full rather than executing the candidate's throwing module.
            self.assertEqual(selected["profile"], "full")
            self.assertEqual(selected['policy_origin'], 'base')

    def test_policy_rollout_pin_applies_only_to_exact_head_and_never_weakens_full(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, text=True,
                                               stderr=subprocess.DEVNULL).strip()
            git('init', '-b', 'main')
            git('config', 'user.email', 'test@example.test')
            git('config', 'user.name', 'Policy Test')
            selector = root / 'automation/langflow/gophercrm_runner/validation.py'
            selector.parent.mkdir(parents=True)
            source = (ROOT / 'automation/langflow/gophercrm_runner/validation.py').read_text()
            selector.write_text(source)
            smoke = root / 'gocrm-ui' / SMOKE
            smoke.parent.mkdir(parents=True)
            smoke.write_text('// fixture\n')
            git('add', '.')
            git('commit', '-m', 'base')
            base = git('rev-parse', 'HEAD')
            selector.write_text(source.replace(f'POLICY_VERSION = {POLICY_VERSION}', 'POLICY_VERSION = 77'))
            git('add', '.')
            git('commit', '-m', 'reviewed policy')
            head = git('rev-parse', 'HEAD')
            event = root / 'event.json'
            event.write_text(json.dumps({'pull_request': {'base': {'sha': base}, 'head': {'sha': head}}}))
            for pin, full, version, profile in [('', 'false', POLICY_VERSION, 'runner'), (base, 'false', POLICY_VERSION, 'runner'),
                    (head, 'false', 77, 'runner'), (head, 'true', 77, 'full')]:
                env = dict(os.environ, GITHUB_EVENT_PATH=str(event), GITHUB_EVENT_NAME='pull_request',
                    GITHUB_OUTPUT=str(root / 'output'), RUNNER_TEMP=folder,
                    REVIEWED_VALIDATION_POLICY_SHA=pin, FORCE_FULL=full)
                env.pop('GITHUB_STEP_SUMMARY', None)
                subprocess.run([sys.executable, str(ROOT / 'scripts/ci/plan-validation.py')],
                    cwd=root, env=env, check=True, capture_output=True)
                result = json.loads((root / 'validation-plan.json').read_text())
                self.assertEqual((result['policy_version'], result['profile']), (version, profile))
                self.assertEqual(result['policy_commit'], head if pin == head else base)

            # Actions checks out a synthetic merge; the reviewed branch SHA
            # still pins its policy only when both event parents match.
            git('checkout', '-b', 'updated-main', base)
            (root / 'README.md').write_text('Main advanced.\n')
            git('add', '.')
            git('commit', '-m', 'main update')
            updated_base = git('rev-parse', 'HEAD')
            git('merge', '--no-ff', 'main', '-m', 'synthetic PR merge')
            merged = git('rev-parse', 'HEAD')
            for event_base, candidate, version in [(updated_base, head, 77),
                    (base, head, POLICY_VERSION), (updated_base, base, POLICY_VERSION)]:
                event.write_text(json.dumps({'pull_request': {
                    'base': {'sha': event_base}, 'head': {'sha': candidate}}}))
                env.update(REVIEWED_VALIDATION_POLICY_SHA=head, FORCE_FULL='false')
                subprocess.run([sys.executable, str(ROOT / 'scripts/ci/plan-validation.py')],
                    cwd=root, env=env, check=True, capture_output=True)
                result = json.loads((root / 'validation-plan.json').read_text())
                self.assertEqual(result['policy_version'], version)
                self.assertEqual(result['head'], merged)

            # A merged main push uses its reviewed policy; other refs keep base.
            for ref, version in [('refs/heads/main', 77), ('refs/heads/feature', POLICY_VERSION)]:
                event.write_text(json.dumps({'before': updated_base, 'ref': ref}))
                env.update(GITHUB_EVENT_NAME='push', REVIEWED_VALIDATION_POLICY_SHA='')
                subprocess.run([sys.executable, str(ROOT / 'scripts/ci/plan-validation.py')],
                    cwd=root, env=env, check=True, capture_output=True)
                result = json.loads((root / 'validation-plan.json').read_text())
                self.assertEqual(result['policy_version'], version)

    def test_ci_without_trusted_base_falls_back_to_full(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            event = root / "event.json"
            event.write_text(json.dumps({"before": "0" * 40}))
            output = root / "output"
            env = dict(os.environ, GITHUB_EVENT_PATH=str(event), GITHUB_EVENT_NAME="push",
                       GITHUB_OUTPUT=str(output), RUNNER_TEMP=folder)
            env.pop("GITHUB_STEP_SUMMARY", None)
            subprocess.run([sys.executable, str(ROOT / "scripts/ci/plan-validation.py")],
                           cwd=ROOT, env=env, check=True, capture_output=True, text=True)
            self.assertIn("profile=full", output.read_text())
            self.assertEqual(json.loads((root / "validation-plan.json").read_text())["specs"], [])


if __name__ == "__main__":
    unittest.main()
