import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]

def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

release = load('release', ROOT / 'scripts/release/release.py')
sql = load('sql_coverage', ROOT / 'scripts/release/check-sql-tests.py')


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sha = 'a' * 40
        self.manifest = {'schema': 1, 'repository': release.REPO, 'revision': self.sha,
                         'run_id': '123', 'version': '1.3.0', 'validation_profile': 'full',
                         'publish': True, 'artifact': release.BUNDLE}

    def bundle(self, extra=None, revision=None):
        text = json.dumps(self.manifest)
        (self.root / 'release-manifest.json').write_text(text)
        members = {'bin/gophercrm': 'binary', 'bin/create-admin': 'binary', 'bin/migrate': 'binary',
                   'www/index.html': 'html', 'REVISION': revision or self.sha,
                   'release-manifest.json': text, 'CHANGELOG.md': 'notes', 'LICENSE': 'MIT'}
        members.update(extra or {})
        with tarfile.open(self.root / release.BUNDLE, 'w:gz') as archive:
            for name, value in members.items():
                item = tarfile.TarInfo(name)
                item.size = len(value.encode())
                archive.addfile(item, io.BytesIO(value.encode()))
        release.seal(self.root)

    def test_stable_semver_only(self):
        self.assertEqual((1, 20, 3), release.version('1.20.3'))
        for value in ['v1.2.0', '01.2.0', '1.2.0-beta.1', '1.2.0\n', '1.2', '$(touch bad)']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                release.version(value)

    def test_prepare_moves_notes_and_versions_without_tagging(self):
        subprocess.run(['git', 'init', '-q', str(self.root)], check=True)
        (self.root / 'gocrm-ui').mkdir()
        (self.root / 'cmd').mkdir()
        (self.root / 'api').mkdir()
        (self.root / 'cmd/main.go').write_text('// @version 1.2.0\n')
        (self.root / 'api/swagger.json').write_text('        "version": "1.2.0"\n')
        (self.root / 'api/swagger.yaml').write_text('  version: 1.2.0\n')
        for name in ('package.json', 'package-lock.json'):
            (self.root / 'gocrm-ui' / name).write_text(json.dumps({'version': '1.2.0', 'packages': {'': {'version': '1.2.0'}}}))
        (self.root / 'CHANGELOG.md').write_text('# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- A fix\n\n## [1.2.0] - 2026-01-01\n\n- Old\n\n'
            f'[Unreleased]: https://github.com/{release.REPO}/compare/v1.2.0...HEAD\n')
        subprocess.run(['git', '-C', str(self.root), 'add', '.'], check=True)
        subprocess.run(['git', '-C', str(self.root), '-c', 'user.name=Test', '-c', 'user.email=test@example.test', 'commit', '-qm', 'fixture'], check=True)
        release.prepare(self.root, '1.3.0')
        text = (self.root / 'CHANGELOG.md').read_text()
        self.assertIn('- A fix', release.notes(text, '1.3.0'))
        self.assertNotIn('- Old', release.notes(text, '1.3.0'))
        self.assertIn('/compare/v1.3.0...HEAD', text)
        self.assertEqual('1.3.0', json.loads((self.root / 'gocrm-ui/package-lock.json').read_text())['packages']['']['version'])
        release.api_versions(self.root, '1.3.0')
        with self.assertRaisesRegex(ValueError, 'API version'):
            release.api_versions(self.root, '1.2.0')
        self.assertEqual('', release.run('git', '-C', str(self.root), 'tag'))
        with self.assertRaisesRegex(ValueError, 'clean worktree'):
            release.prepare(self.root, '1.4.0')

    def test_verify_checks_identity_checksum_and_archive_metadata(self):
        self.bundle()
        self.assertEqual(self.manifest, release.verify(self.root, self.sha, '123', 'v1.3.0'))
        with self.assertRaisesRegex(ValueError, 'identity'):
            release.verify(self.root, 'b' * 40)
        with self.assertRaisesRegex(ValueError, 'identity'):
            release.verify(self.root, run_id='456')
        (self.root / release.BUNDLE).write_bytes(b'corrupted')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            release.verify(self.root)
        self.bundle(revision='b' * 40)
        with self.assertRaisesRegex(ValueError, 'metadata differs'):
            release.verify(self.root)

    def test_verify_refuses_traversal_links_and_duplicate_members(self):
        self.bundle({'../../outside': 'bad'})
        with self.assertRaisesRegex(ValueError, 'Unsafe'):
            release.verify(self.root)
        for kind in ('link', 'duplicate'):
            self.bundle()
            with tarfile.open(self.root / release.BUNDLE, 'r:gz') as archive:
                data = [(m, archive.extractfile(m).read()) for m in archive.getmembers()]
            with tarfile.open(self.root / release.BUNDLE, 'w:gz') as archive:
                for member, content in data:
                    archive.addfile(member, io.BytesIO(content))
                member = tarfile.TarInfo('symlink' if kind == 'link' else 'REVISION')
                if kind == 'link':
                    member.type, member.linkname = tarfile.SYMTYPE, '/etc/passwd'
                archive.addfile(member)
            release.seal(self.root)
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, 'Unsafe'):
                release.verify(self.root)

    def test_tag_check_refuses_duplicates_older_versions_and_network_failure(self):
        for tag in ('v1.3.0', 'v2.0.0'):
            with patch.object(release, 'run', return_value=f'{self.sha}\trefs/tags/{tag}'), self.assertRaises(ValueError):
                release.ensure_new_tag('v1.3.0')
        with patch.object(release, 'run', side_effect=subprocess.CalledProcessError(1, 'git')), self.assertRaises(subprocess.CalledProcessError):
            release.ensure_new_tag('v1.3.0')
        with patch.object(release, 'run', return_value=''), patch.object(release, 'gh_json', return_value=[[{'tag_name': 'v1.3.0'}]]), self.assertRaisesRegex(ValueError, 'draft'):
            release.ensure_new_tag('v1.3.0')

    def test_publication_checks_context_before_any_mutation(self):
        self.bundle()
        for env in ({}, {'GITHUB_REF': 'refs/heads/main', 'GITHUB_SHA': 'b' * 40, 'GITHUB_RUN_ID': '123'}):
            with patch.dict(os.environ, env, clear=True), patch.object(release, 'run') as command, self.assertRaises(ValueError):
                release.publish(self.root, self.root / 'notes.md')
            command.assert_not_called()

    def test_publication_failure_leaves_draft_unpublished(self):
        self.bundle()
        env = {'GITHUB_REF': 'refs/heads/main', 'GITHUB_SHA': self.sha, 'GITHUB_RUN_ID': '123'}
        calls = []
        def command(*args):
            calls.append(args)
            if args[:3] == ('gh', 'release', 'download'):
                raise RuntimeError('asset verification unavailable')
            return ''
        with patch.dict(os.environ, env), patch.object(release, 'ensure_new_tag'), patch.object(release, 'run', side_effect=command), self.assertRaises(RuntimeError):
            release.publish(self.root, self.root / 'notes.md')
        self.assertTrue(any('--draft' in call for call in calls))
        self.assertFalse(any('--draft=false' in call for call in calls))
        self.assertFalse((self.root / 'publication.json').exists())

    def test_publication_receipt_lookup_handles_multiple_pages_and_expiry(self):
        pages = [{'artifacts': [{'name': 'build', 'expired': False}]},
                 {'artifacts': [{'name': 'release-publication', 'expired': True}]},
                 {'artifacts': [{'name': 'release-publication', 'expired': False}]}]
        with patch.object(release, 'gh_json', return_value=pages) as api:
            self.assertEqual(1, release.publication_count('123'))
            args = api.call_args.args
            self.assertIn('--slurp', args)
            self.assertNotIn('--jq', args)
        with patch.object(release, 'gh_json', return_value=pages[:2]):
            self.assertEqual(0, release.publication_count('123'))
        with self.assertRaises(ValueError):
            release.publication_count('../other')

    def test_deploy_rejects_old_or_unvalidated_release(self):
        self.bundle()
        current = {'draft': False, 'prerelease': False, 'tag_name': 'v1.3.0'}
        with patch.object(release, 'gh_json', side_effect=[current, {'tag_name': 'v1.4.0'}]), self.assertRaisesRegex(ValueError, 'latest'):
            release.download(self.root, tag='v1.3.0')
        run_info = {'path': '.github/workflows/ci.yml', 'conclusion': 'success', 'event': 'push', 'head_branch': 'main', 'head_sha': self.sha}
        def command(*args):
            return f'{self.sha}\trefs/tags/v1.3.0' if args[:2] == ('git', 'ls-remote') else ''
        with patch.object(release, 'gh_json', side_effect=[current, current, run_info]), patch.object(release, 'run', side_effect=command), self.assertRaisesRegex(ValueError, 'full release workflow'):
            release.download(self.root, tag='v1.3.0')
        run_info.update(path='.github/workflows/release.yml', event='workflow_dispatch')
        with patch.object(release, 'gh_json', side_effect=[current, current, run_info]), patch.object(release, 'run', side_effect=command):
            self.assertEqual(self.manifest, release.download(self.root, tag='v1.3.0'))

    def test_sql_release_coverage_requires_unskipped_real_roots(self):
        folder = self.root / 'internal/integration'
        folder.mkdir(parents=True)
        (folder / 'example_test.go').write_text('func TestExampleMySQL(t *testing.T) {}\n')
        events = [{'Action': 'pass', 'Package': 'github.com/florinel-chis/gophercrm/cmd/migrate', 'Test': name} for name in
                  ['TestMigrationFresh_FullUpAndDown', 'TestMigrationUpgrade_FromMainAutoMigratedSchema']]
        events.append({'Action': 'pass', 'Package': 'github.com/florinel-chis/gophercrm/internal/integration', 'Test': 'TestExampleMySQL'})
        def output():
            return '\n'.join(json.dumps(event) for event in events)
        sql.check(output(), self.root)
        events[-1]['Action'] = 'skip'
        with self.assertRaisesRegex(ValueError, 'skipped'):
            sql.check(output(), self.root)
