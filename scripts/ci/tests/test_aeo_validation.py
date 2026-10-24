import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'automation/langflow'))
from gophercrm_runner import validation as v


class AEOSelectionTests(unittest.TestCase):
    def test_scope_requires_proofs_and_roots_and_preserves_full_and_mixed_gates(self):
        paths = ['cmd/main.go', 'internal/service/aeo_service.go', 'internal/repository/interfaces.go',
                 'cmd/aeo_runtime_test.go', 'internal/aeo/scheduler.go', 'docs/DEVELOPMENT.md']
        proof = dict(aeo_main=True, aeo_interface=True, aeo_roots=True)
        self.assertEqual(v.plan(paths)['profile'], 'full')
        for missing in proof:
            self.assertEqual(v.plan(paths, **dict(proof, **{missing: False}))['profile'], 'full')
        selected = v.plan(paths, **proof)
        self.assertEqual(selected['profile'], 'aeo')
        self.assertEqual(set(selected['specs']), v.AEO_SPECS)
        checks = v.core_commands([('make', 'verify'), ('custom',)], selected)
        self.assertIn(('python3', 'scripts/ci/check-go.py', 'aeo'), checks)
        self.assertIn(('custom',), checks)
        self.assertNotIn(('make', 'verify'), checks)
        self.assertEqual(v.plan(paths, full=True, **proof)['profile'], 'full')
        for extra in ['internal/middleware/auth.go', 'internal/service/auth_service.go', 'internal/models/aeo.go',
                      'internal/config/config.go', 'internal/service/customer_service.go', 'migrations/021.sql',
                      'cmd/other.go', 'go.mod', 'gocrm-ui/src/pages/aeo/AEO.tsx', 'gocrm-ui/src/components/Table.tsx']:
            with self.subTest(extra=extra):
                self.assertEqual(v.plan(paths + [extra], **proof)['profile'], 'full')
        selected = v.plan(paths + ['scripts/ci/check-go.py'], **proof)
        self.assertIn(('make', 'verify-runner'), v.core_commands([], selected))

    def test_main_proof_and_required_roots_use_actual_git_candidates(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, stderr=subprocess.DEVNULL, text=True).strip()
            git('init'); git('config', 'user.email', 'test@example.test'); git('config', 'user.name', 'Test')
            source = (ROOT / 'cmd/main.go').read_text()
            main = root / 'cmd/main.go'; main.parent.mkdir(); main.write_text(source)
            for package, names in v.AEO_REQUIRED_ROOTS.items():
                file = root / package / 'aeo_runtime_test.go'; file.parent.mkdir(parents=True, exist_ok=True)
                file.write_text('package example\nimport "testing"\n' + ''.join(
                    f'func {name}(t *testing.T) {{}}\n' for name in sorted(names)))
            git('add', '.'); git('commit', '-m', 'base'); base = git('rev-parse', 'HEAD')
            allowed = source.replace(' && len(aeoProviders) > 0', '')
            threaded = allowed.replace('router := setupRouter(backgroundCtx, cfg)', 'router, stopAEO := setupRouter(backgroundCtx, cfg)').replace(
                'cfg *config.Config) *gin.Engine', 'cfg *config.Config) (*gin.Engine, func(context.Context) error)').replace(
                '{\n\t\tsetupDependencies(backgroundCtx, api, cfg)\n\t}', 'stopAEO := setupDependencies(backgroundCtx, api, cfg)').replace(
                'return router', 'return router, stopAEO').replace(
                'func setupDependencies(backgroundCtx context.Context, router *gin.RouterGroup, cfg *config.Config) {',
                'func setupDependencies(backgroundCtx context.Context, router *gin.RouterGroup, cfg *config.Config) func(context.Context) error {')
            variants = [
                (threaded, True),
                (threaded.replace('stopAEO := setupDependencies', 'router = setupDependencies'), False),
                (threaded.replace('router, stopAEO := setupRouter', 'router, cfg := setupRouter'), False),
                (threaded.replace('func(context.Context) error', '*gin.Engine'), False),
                (allowed, True),
                (allowed.replace('shutdownErr := srv.Shutdown(ctx)', 'shutdownErr := drainAEOAndHTTP(ctx, srv)'), True),
                (allowed.replace('router := setupRouter(backgroundCtx, cfg)', 'router, stopAEO := setupRouter(backgroundCtx, cfg)'), True),
                (allowed.replace('cfg.Server.TrustedProxies', 'nil'), False),
                (allowed.replace('models.InitDatabase(&cfg.Database)', 'models.InitDatabase(nil)'), False),
                (allowed.replace('service.NewUserService(userRepo)', 'service.NewUserService(nil)'), False),
                (allowed.replace('middleware.Auth(authService)', 'middleware.Auth(nil)'), False),
                (allowed.replace('setupDependencies(backgroundCtx, api, cfg)', 'setupDependencies(backgroundCtx, router, cfg)'), False),
                (allowed.replace('setupDependencies(backgroundCtx, api, cfg)', ''), False),
                (allowed.replace('return router', 'return gin.New()'), False),
                (allowed.replace('setupRouter(backgroundCtx, cfg)', 'setupRouter(context.Background(), cfg)'), False),
                (allowed + '\nfunc unrelated() {}\n', False),
                (allowed.replace('// Public routes with strict rate limiting', 'return\n\t// Public routes with strict rate limiting'), False),
                (allowed.replace('router := gin.New()', 'router := gin.New()\nreturn router').replace('\n\treturn router\n', '\n'), False),
            ]
            for body, expected in variants:
                self.assertNotEqual(body, source)
                main.write_text(body); git('add', '.'); git('commit', '--allow-empty', '-m', 'candidate')
                head = git('rev-parse', 'HEAD')
                with self.subTest(expected=expected, body=body[-120:]):
                    self.assertEqual(v.aeo_main_only(base, head, cwd=root), expected)
            self.assertTrue(v.aeo_roots_present(base, cwd=root))
            file = root / 'cmd/aeo_runtime_test.go'
            file.write_text(file.read_text().replace('TestAEORuntimeShutdown', 'TestAbsent'))
            git('add', '.'); git('commit', '-m', 'missing root')
            self.assertFalse(v.aeo_roots_present(git('rev-parse', 'HEAD'), cwd=root))
            main.unlink(); git('add', '.'); git('commit', '-m', 'deleted main')
            self.assertFalse(v.aeo_main_only(base, git('rev-parse', 'HEAD'), cwd=root))
            with patch.object(v.subprocess, 'run', side_effect=FileNotFoundError):
                # Invalid references fail before probing tools.
                self.assertFalse(v.aeo_main_only('HEAD', base, cwd=root))

    def test_explicit_full_keeps_sql_callers_without_narrowing_or_future_root_bypass(self):
        selected = v.plan(['cmd/main.go'], full=True)
        self.assertEqual(selected['profile'], 'full')
        self.assertEqual(selected['specs'], [])
        checks = v.core_commands([], selected, explicit_full=True)
        self.assertEqual(checks, [('make', 'verify'), ('python3', 'scripts/ci/check-go.py', 'transaction')])
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        self.assertEqual(workflow.count('if [ "$VALIDATION_PROFILE" = full ]; then'), 2)
        self.assertEqual(workflow.count('            python3 scripts/ci/check-go.py transaction\n'), 2)
        self.assertIn("full: true", (ROOT / '.github/workflows/release.yml').read_text())
        # Legacy callers remain registered exactly as before.
        self.assertEqual(v.core_commands([], selected), [('make', 'verify')])

    def test_shared_interface_mask_does_not_hide_other_declarations(self):
        source = 'package repository\ntype Other interface { Read() }\ntype AEORepository interface { Get() }\n'
        mask = lambda value: v.without_refresh_interface_body(value, 'AEORepository')
        self.assertEqual(mask(source), mask(source.replace('Get()', 'WithContext()')))
        self.assertNotEqual(mask(source), mask(source.replace('Read()', 'Write()')))
        self.assertNotEqual(mask(source), mask(source.replace('Get()', 'Get() };func bad(){')))
