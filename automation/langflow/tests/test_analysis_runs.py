"""Run identity, verified journals, worktree and guidance for the feature analysis workflow."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from gophercrm_runner.config import Config, GateError
from gophercrm_runner.process import Commands
from gophercrm_runner import analysis, contracts


def git(cwd, *args):
    return subprocess.check_output(['git', *args], cwd=cwd, text=True,
                                   env={**os.environ, 'GIT_AUTHOR_NAME': 't', 'GIT_AUTHOR_EMAIL': 't@example.test',
                                        'GIT_COMMITTER_NAME': 't', 'GIT_COMMITTER_EMAIL': 't@example.test'}).strip()


def make_checkout(root):
    """A checkout with one commit on main, a bare origin and an untracked AGENTS.md."""
    checkout = root / 'checkout'
    checkout.mkdir()
    git(checkout, 'init', '-q', '-b', 'main')
    (checkout / 'docs').mkdir()
    (checkout / 'docs' / 'DEVELOPMENT.md').write_text('# Guide\n\nCommitted guidance.\n')
    (checkout / 'internal').mkdir()
    (checkout / 'internal' / 'service.go').write_text('package service\n\nfunc Lead() {}\n')
    (checkout / '.gitignore').write_text('docs/specs/\nAGENTS.md\n')
    git(checkout, 'add', '-A')
    git(checkout, 'commit', '-q', '-m', 'init')
    origin = root / 'origin.git'
    git(root, 'clone', '-q', '--bare', str(checkout), str(origin))
    git(checkout, 'remote', 'add', 'origin', str(origin))
    git(checkout, 'fetch', '-q', 'origin')
    (checkout / 'AGENTS.md').write_text('Operator guidance.\n')
    return checkout


def make_config(root, checkout):
    policy = root / 'policy.json'
    policy.write_text(json.dumps(dict(repository='example/repo', checkout=str(checkout),
                                      state_dir=str(root / 'state'), issues=[95])))
    return Config.load(policy)


class RemoteFakingCommands:
    """Real commands, except that origin reports the trusted GitHub URL."""
    def __init__(self, root):
        self.real = Commands(root / 'logs')

    async def run(self, args, **kwargs):
        if tuple(args) == ('git', 'remote', 'get-url', 'origin'):
            return 0, 'https://github.com/example/repo.git\n'
        return await self.real.run(args, **kwargs)


class RunModelTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.checkout = make_checkout(self.root)
        self.config = make_config(self.root, self.checkout)
        self.base = git(self.checkout, 'rev-parse', 'origin/main')
        self.commands = RemoteFakingCommands(self.root)

    def tearDown(self):
        self.temp.cleanup()

    def test_new_run_and_load_run_round_trip(self):
        run = analysis.new_run(self.config, base=self.base, wish='Add quotes', links=['https://x.test/a'],
                               comments='')
        self.assertRegex(run.run_id, '^[0-9a-f]{32}$')
        self.assertEqual((run.revision, run.parent, run.inherited, run.base), (1, None, (), self.base))
        loaded = analysis.load_run(self.config, run.run_id)
        self.assertEqual(loaded, run)
        record = analysis.read_run(run)
        self.assertEqual(record['wish'], 'Add quotes')
        self.assertEqual(record['comments'], [])
        self.assertEqual(record['calls'], [])
        self.assertIsNone(record['document'])
        analysis.update_run(run, slug='add-quotes')
        self.assertEqual(analysis.load_run(self.config, run.run_id).slug, 'add-quotes')

    def test_load_run_refuses_other_checkout_and_bad_ids(self):
        run = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='')
        other = self.root / 'other'
        (other / '.git').mkdir(parents=True)
        policy = self.root / 'other.json'
        policy.write_text(json.dumps(dict(repository='example/repo', checkout=str(other),
                                          state_dir=str(self.root / 'state'), issues=[95])))
        with self.assertRaises(GateError):
            analysis.load_run(Config.load(policy), run.run_id)
        for bad in ('', '../x', 'f' * 31, 'F' * 32):
            with self.subTest(bad=bad), self.assertRaises(GateError):
                analysis.load_run(self.config, bad)

    def test_numbered_comments(self):
        self.assertEqual(analysis.numbered_comments('  first\nline\n\n\n second \n\n'),
                         [{'id': 'C-1', 'text': 'first\nline'}, {'id': 'C-2', 'text': 'second'}])
        self.assertEqual(analysis.numbered_comments('\n \n'), [])

    def test_record_invalidates_later_stages_and_recall_verifies_chain(self):
        run = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='')
        analysis.record(run, 'intake', {'brief': 1}, '# Brief\n', ['n1'], {})
        intake = analysis.recall(run, 'intake')
        self.assertEqual((intake['data'], intake['notes']), ({'brief': 1}, ['n1']))
        analysis.record(run, 'research', {'r': 2}, '# R\n', [], {'intake': intake['sha256']})
        analysis.record(run, 'analysis', {'a': 3}, '# A\n', [], {'research': analysis.recall(run, 'research')['sha256']})
        self.assertTrue((run.directory / 'analysis.json').exists())
        analysis.record(run, 'research', {'r': 4}, '# R2\n', [], {'intake': intake['sha256']})
        self.assertFalse((run.directory / 'analysis.json').exists())
        self.assertFalse((run.directory / 'analysis.md').exists())
        with self.assertRaises(GateError):
            analysis.recall(run, 'analysis')
        path = run.directory / 'intake.json'
        path.write_text(path.read_text().replace('"brief": 1', '"brief": 9'))
        with self.assertRaisesRegex(GateError, 'changed'):
            analysis.recall(run, 'research')
        md = run.directory / 'research.md'
        md.write_text('tampered')
        with self.assertRaises(GateError):
            analysis.recall(run, 'research')

    def test_inherited_stage_is_read_from_parent(self):
        parent = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='')
        analysis.record(parent, 'intake', {'brief': 1}, '# Brief\n', [], {})
        parent_intake = analysis.recall(parent, 'intake')
        child = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='change it',
                                 parent=parent, refine_from='research')
        self.assertEqual((child.revision, child.parent, child.inherited), (2, parent.run_id, ('intake',)))
        self.assertEqual(analysis.read_run(child)['comments'], [{'id': 'C-1', 'text': 'change it'}])
        inherited = analysis.recall(child, 'intake')
        self.assertEqual(inherited['data'], {'brief': 1})
        self.assertEqual(inherited['sha256'], parent_intake['sha256'])
        with self.assertRaisesRegex(GateError, 'inherited'):
            analysis.record(child, 'intake', {}, '', [], {})
        analysis.record(child, 'research', {'r': 1}, '# R\n', [],
                        {'intake': {'run_id': parent.run_id, 'sha256': inherited['sha256']}})
        self.assertEqual(analysis.recall(child, 'research')['data'], {'r': 1})
        path = parent.directory / 'intake.json'
        path.write_text(path.read_text().replace('"brief": 1', '"brief": 2'))
        with self.assertRaisesRegex(GateError, 'changed'):
            analysis.recall(child, 'research')

    def test_run_lock_is_exclusive(self):
        run = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='')
        with analysis.run_lock(run):
            with self.assertRaisesRegex(GateError, 'active'):
                with analysis.run_lock(run):
                    pass
        with analysis.run_lock(run):
            pass

    async def test_resolve_base_fetches_origin_main(self):
        (self.checkout / 'new.txt').write_text('x\n')
        git(self.checkout, 'add', 'new.txt')
        git(self.checkout, 'commit', '-q', '-m', 'second')
        git(self.checkout, 'push', '-q', 'origin', 'main')
        head = git(self.checkout, 'rev-parse', 'HEAD')
        self.assertEqual(await analysis.resolve_base(self.commands, self.config), head)
        untrusted = Commands(self.root / 'logs')
        with self.assertRaisesRegex(GateError, 'origin'):
            await analysis.resolve_base(untrusted, self.config)

    async def test_worktree_add_reuse_remove(self):
        run = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='')
        worktree = await analysis.ensure_worktree(self.commands, run)
        self.assertEqual(worktree, run.directory / 'worktree')
        self.assertEqual(git(worktree, 'rev-parse', 'HEAD'), self.base)
        self.assertFalse((worktree / 'AGENTS.md').exists())
        self.assertEqual(await analysis.ensure_worktree(self.commands, run), worktree)
        git(worktree, 'checkout', '-q', '--detach', 'HEAD~0')
        (worktree / 'extra.txt').write_text('x')
        git(worktree, 'add', 'extra.txt')
        git(worktree, 'commit', '-q', '-m', 'moved')
        with self.assertRaises(GateError):
            await analysis.ensure_worktree(self.commands, run)
        await analysis.remove_worktree(self.commands, run)
        self.assertFalse(worktree.exists())
        self.assertNotIn('worktree', git(self.checkout, 'worktree', 'list'))
        self.assertEqual(await analysis.ensure_worktree(self.commands, run), worktree)
        await analysis.remove_worktree(self.commands, run)

    async def test_guidance_at_base_ignores_working_tree_edit(self):
        (self.checkout / 'docs' / 'DEVELOPMENT.md').write_text('# Guide\n\nUncommitted edit.\n')
        documents = await analysis.guidance_at_base(self.commands, self.config, self.base)
        self.assertEqual([d['path'] for d in documents], ['AGENTS.md', 'docs/DEVELOPMENT.md'])
        self.assertEqual(documents[0], {'path': 'AGENTS.md', 'origin': 'operator-local', 'content': 'Operator guidance.\n'})
        self.assertEqual(documents[1]['origin'], 'git')
        self.assertIn('Committed guidance.', documents[1]['content'])
        self.assertNotIn('Uncommitted', documents[1]['content'])
        self.assertRegex(contracts.context_hash(documents), '^[0-9a-f]{64}$')
        (self.checkout / 'AGENTS.md').unlink()
        with self.assertRaisesRegex(GateError, 'AGENTS.md'):
            await analysis.guidance_at_base(self.commands, self.config, self.base)

    def test_citations(self):
        run = analysis.new_run(self.config, base=self.base, wish='w', links=[], comments='')
        self.assertTrue(analysis.inside_checkout(self.checkout, 'internal/service.go'))
        self.assertFalse(analysis.inside_checkout(self.checkout, '../checkout/internal/service.go'))
        self.assertFalse(analysis.inside_checkout(self.checkout, '/etc/hosts'))
        self.assertFalse(analysis.inside_checkout(self.checkout, '.git/config'))
        self.assertEqual(analysis.cited_line(self.checkout, 'internal/service.go', 3), 'func Lead() {}')
        self.assertIsNone(analysis.cited_line(self.checkout, 'internal/service.go', 9))
        self.assertIsNone(analysis.cited_line(self.checkout, 'missing.go', 1))
        self.assertIsNone(analysis.cited_line(self.checkout, 'internal/service.go', 0))


if __name__ == '__main__':
    unittest.main()
