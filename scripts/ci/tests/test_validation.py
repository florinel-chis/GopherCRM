import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "automation/langflow"))
from gophercrm_runner.validation import browser_command, changed_paths, plan, SMOKE


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
        for path in ["internal/service/auth_service.go", "internal/middleware/auth.go",
                     "internal/models/ticket.go", "internal/service/interfaces.go",
                     "internal/handler/routes.go", "migrations/new.sql", "go.mod",
                     "gocrm-ui/package-lock.json", "gocrm-ui/src/components/Table.tsx",
                     "automation/langflow/gophercrm_runner/validation.py",
                     "scripts/ci/plan-validation.py", ".github/workflows/ci.yml", "new-feature.bin"]:
            with self.subTest(path=path):
                self.assertEqual(plan([path])["profile"], "full")
                self.assertEqual(plan([path])["specs"], [])
        self.assertEqual(plan([])["profile"], "full")
        self.assertEqual(plan(["README.md"], full=True)["profile"], "full")
        self.assertEqual(plan(["docs/DEVELOPMENT.md"])["specs"], [SMOKE])

    def test_paths_cannot_inject_commands_or_github_outputs(self):
        for path in ["../secret", "/tmp/file", "docs/x\nspecs=evil.md", r"..\file", "x\0y"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                plan([path])
        self.assertEqual(plan(["gocrm-ui/e2e/tests/$(touch pwned).spec.ts"])["profile"], "full")

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
            self.assertEqual(selected["profile"], "full")
            self.assertIn("shared or validation code", selected["reasons"][0])

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
