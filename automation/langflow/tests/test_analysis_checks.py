"""Stage schemas, deterministic checks and the document renderer of the analysis workflow."""

import copy
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest

from jsonschema import Draft202012Validator

from gophercrm_runner import analysis, analysis_render
from gophercrm_runner.config import GateError


def brief():
    return dict(title='Follow-up automation', slug='follow-up-automation',
                restatement='Rules that react to lead and deal events.',
                project_terms=[dict(term='lead', meaning='a prospect', paths=['internal/service.go'])],
                in_scope=['rules'], out_of_scope=['billing'], assumptions=['one tenant'], open_questions=['which mail'])


def research():
    return dict(summary='Leads exist; no rules engine.',
                repository_findings=[dict(claim='Lead service exists', file='internal/service.go', line=3)],
                conventions=[dict(constraint='Unified responses', file='docs/DEVELOPMENT.md', line=1)],
                external_findings=[dict(claim='Frappe offers follow-up rules', url='https://frappe.io/crm/follow-ups-automation',
                                        title='Follow-ups & Automation')],
                prior_art=[dict(name='Frappe CRM', url='https://frappe.io/crm', summary='Rules', relevance='Direct')],
                gaps=['No scheduler'])


def analysis_output(words=None):
    problem = words or 'Sales forgets to follow up.'
    return dict(problem=problem, users=[dict(role='sales', need='reminders')], proposal='Add rules.',
                in_scope=['rules'], out_of_scope=['billing'], main_flow=['create rule', 'event fires', 'task created'],
                options_considered=[dict(option='manual tasks', why_not='forgotten')],
                risks=[dict(risk='mail storms', mitigation='rate limit')], size=dict(t_shirt='M', basis='three entities'),
                decision_needed=['approve scope'], comment_dispositions=[])


def manifest():
    return dict(schema_version=1, repository='example/repo', issue=0, objective='Automate follow-ups.',
                non_goals=['billing'], scope_paths=['internal/service/*', 'internal/repository/*', 'migrations/*'],
                dependencies=[], validation_profile='backend', context_refs=['AGENTS.md', 'docs/DEVELOPMENT.md'],
                acceptance=[dict(id='AC-1', requirement='A rule fires on lead creation.',
                                 verification=dict(kind='automated', check_id='followup-rules')),
                            dict(id='AC-2', requirement='A rule never fires twice for one event.',
                                 verification=dict(kind='automated', check_id='followup-rules'))])


def specification():
    return dict(manifest=manifest(),
                acceptance_checks=[dict(check_id='followup-rules',
                                        argv=['go', 'test', '-json', '-count=1', './internal/service', '-run', '^TestFollowUpRule(Fires|Once)$'],
                                        tests=[dict(package='example/repo/internal/service', name='TestFollowUpRuleFires'),
                                               dict(package='example/repo/internal/service', name='TestFollowUpRuleOnce')])],
                ui_criteria=[dict(id='UI-1', requirement='Rules page lists rules.', verification=dict(kind='browser', detail='/rules shows the table'))],
                data_model=dict(entities=[dict(name='FollowUpRule', table='follow_up_rules',
                                               columns=[dict(name='id', type='uint', nullable=False, note='pk')],
                                               indexes=['idx_follow_up_rules_event'], relations=['owner -> users'])],
                                auto_migrate=['models.FollowUpRule'],
                                sql_migration=dict(name='20261101120000_follow_up_rules', up=['CREATE TABLE follow_up_rules (...)'],
                                                   down=['DROP TABLE follow_up_rules'])),
                api=[dict(method='POST', path='/rules', roles=['admin', 'sales'], request='{name, trigger}', response='rule', errors=['400', '409'])],
                screens=[dict(route='/rules', page='Rules', purpose='manage rules', elements=['table', 'form'])],
                permissions=[dict(role='admin', actions=['create', 'read', 'update', 'delete'])],
                diagrams=[dict(title='Rule lifecycle', kind='state', mermaid='stateDiagram-v2\n  [*] --> Draft')],
                test_plan=[dict(level='go', name='TestFollowUpRuleFires', proves='fires on create'),
                           dict(level='go', name='TestFollowUpRuleOnce', proves='idempotent'),
                           dict(level='e2e', name='rules.spec.ts', proves='page renders')],
                docs_to_update=[dict(path='docs/DEVELOPMENT.md', change='describe rules'),
                                dict(path='docs/testing/15-rules.md', change='new catalog')],
                open_questions=['Which mail template?'], comment_dispositions=[])


def review():
    return dict(verdict='approve', summary='Sound.',
                findings=[dict(severity='P1', section='API', finding='No 403 listed', fix='Add 403 to errors')],
                criteria=[dict(id='AC-1', observable=True, verifiable=True, note=''),
                          dict(id='AC-2', observable=True, verifiable=True, note=''),
                          dict(id='UI-1', observable=True, verifiable=False, note='browser')])


def revision():
    return dict(analysis=analysis_output(), specification=specification(),
                finding_dispositions=[dict(id='F-1', disposition='fixed', note='403 added')])


class SchemaTests(unittest.TestCase):
    def test_schemas_accept_fixtures_and_reject_extra_or_missing_fields(self):
        cases = [(analysis.BRIEF_SCHEMA, brief()), (analysis.RESEARCH_SCHEMA, research()),
                 (analysis.ANALYSIS_SCHEMA, analysis_output()), (analysis.SPECIFICATION_SCHEMA, specification()),
                 (analysis.REVIEW_SCHEMA, review()), (analysis.REVISION_SCHEMA, revision())]
        for schema, fixture in cases:
            with self.subTest(schema=schema['properties'].keys()):
                validator = Draft202012Validator(schema)
                validator.validate(fixture)
                self.assertFalse(validator.is_valid(dict(fixture, extra=1)))
                missing = dict(fixture)
                missing.pop(next(iter(schema['required'])))
                self.assertFalse(validator.is_valid(missing))
        self.assertFalse(Draft202012Validator(analysis.ANALYSIS_SCHEMA).is_valid(
            dict(analysis_output(), size=dict(t_shirt='XXL', basis='x'))))
        self.assertFalse(Draft202012Validator(analysis.REVIEW_SCHEMA).is_valid(dict(review(), verdict='maybe')))


class CheckTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.worktree = Path(self.temp.name)
        (self.worktree / 'internal').mkdir()
        (self.worktree / 'internal' / 'service.go').write_text('package service\n\nfunc Lead() {}\n')
        (self.worktree / 'docs').mkdir()
        (self.worktree / 'docs' / 'DEVELOPMENT.md').write_text('# Guide\n')
        self.config = SimpleNamespace(repository='example/repo', database_test_commands=())

    def tearDown(self):
        self.temp.cleanup()

    def test_check_brief_fixes_slug_and_drops_missing_paths(self):
        value = brief()
        value['slug'] = 'Bad Slug!'
        value['project_terms'][0]['paths'].append('missing/file.go')
        notes = analysis.check_brief(value, self.worktree)
        self.assertEqual(value['slug'], 'follow-up-automation')
        self.assertEqual(value['project_terms'][0]['paths'], ['internal/service.go'])
        self.assertEqual(len(notes), 2)
        self.assertEqual(analysis.check_brief(brief(), self.worktree), [])

    def test_check_research_resolves_citations(self):
        value = research()
        value['repository_findings'].append(dict(claim='Ghost', file='internal/missing.go', line=1))
        value['conventions'].append(dict(claim_or='x', constraint='Nope', file='docs/DEVELOPMENT.md', line=40))
        value['conventions'][-1].pop('claim_or')
        notes = analysis.check_research(value, self.worktree)
        self.assertEqual(value['repository_findings'][0]['text'], 'func Lead() {}')
        self.assertEqual(len(value['repository_findings']), 1)
        self.assertEqual(len(value['conventions']), 1)
        self.assertEqual(value['gaps'][-2:], ['(uncited) Ghost', '(uncited) Nope'])
        self.assertEqual(len(notes), 1)
        with self.assertRaisesRegex(GateError, 'resolvable'):
            analysis.check_research(dict(research(), repository_findings=[dict(claim='x', file='nope', line=1)]), self.worktree)
        with self.assertRaisesRegex(GateError, 'http'):
            analysis.check_research(dict(research(), external_findings=[dict(claim='x', url='ftp://x', title='t')]), self.worktree)

    def test_check_analysis_notes_word_budget_and_unknown_comments(self):
        self.assertEqual(analysis.check_analysis(analysis_output(), []), [])
        long = analysis_output(words=' '.join(['word'] * 520))
        notes = analysis.check_analysis(long, [])
        self.assertEqual(len(notes), 1)
        self.assertIn('500', notes[0])
        value = analysis_output()
        value['comment_dispositions'] = [dict(id='C-9', disposition='applied', note='')]
        self.assertEqual(len(analysis.check_analysis(value, ['C-1'])), 1)

    def test_check_specification_accepts_valid_and_notes_defects(self):
        self.assertEqual(analysis.check_specification(specification(), self.config, self.worktree, []), [])
        cases = {
            'non-contiguous AC': lambda s: s['manifest']['acceptance'][1].update(id='AC-3'),
            'frontend scope': lambda s: s['manifest']['scope_paths'].append('gocrm-ui/src/pages/*'),
            'registry test outside -run': lambda s: s['acceptance_checks'][0]['tests'].append(
                dict(package='example/repo/internal/service', name='TestOther')),
            'go test missing from registry': lambda s: s['test_plan'].append(dict(level='go', name='TestGhost', proves='x')),
            'unknown mermaid': lambda s: s['diagrams'][0].update(mermaid='pie\n  "a": 1'),
            'unregistered check id': lambda s: s['manifest']['acceptance'][0]['verification'].update(check_id='ghost'),
            'bad argv': lambda s: s['acceptance_checks'][0]['argv'].remove('-json'),
            'database profile without commands': lambda s: s['manifest'].update(validation_profile='backend-database'),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name):
                value = specification()
                mutate(value)
                self.assertTrue(analysis.check_specification(value, self.config, self.worktree, []), name)
        value = specification()
        notes = analysis.check_specification(value, self.config, self.worktree, ['C-1'])
        self.assertTrue(any('C-1' in n for n in notes))
        self.assertEqual(value['docs_to_update'][1]['status'], 'new')
        self.assertEqual(value['docs_to_update'][0]['status'], 'exists')

    def test_registry_dict(self):
        self.assertEqual(list(analysis.registry(specification())), ['followup-rules'])
        self.assertEqual(set(analysis.registry(specification())['followup-rules']), {'argv', 'tests'})

    def test_check_review_renumbers_appends_runner_findings_and_overrules(self):
        value = review()
        notes = analysis.check_review(value, ['AC-1', 'AC-2', 'UI-1'], ['one-pager is 520 words, cap 500'])
        self.assertEqual([f['id'] for f in value['findings']], ['F-1', 'F-2'])
        self.assertEqual((value['findings'][1]['severity'], value['findings'][1]['section']), ('P1', 'runner'))
        self.assertEqual(value['verdict'], 'revise')
        self.assertTrue(any('overruled' in n for n in notes))
        value = review()
        value['findings'] = []
        value['criteria'].append(dict(id='AC-9', observable=True, verifiable=True, note=''))
        value['criteria'].pop(0)
        notes = analysis.check_review(value, ['AC-1', 'AC-2', 'UI-1'], [])
        self.assertEqual(value['verdict'], 'approve')
        self.assertEqual([c['id'] for c in value['criteria']], ['AC-2', 'UI-1', 'AC-1'])
        self.assertFalse(value['criteria'][-1]['observable'])
        self.assertEqual(len(notes), 2)

    def test_check_revision_requires_dispositions(self):
        findings = [dict(id='F-1', severity='P0', section='API', finding='x', fix='y'),
                    dict(id='F-2', severity='P2', section='API', finding='x', fix='y')]
        value = revision()
        notes = analysis.check_revision(value, findings)
        self.assertTrue(any('F-2' in n for n in notes))
        value['finding_dispositions'] = [dict(id='F-1', disposition='declined', note='no'),
                                         dict(id='F-2', disposition='fixed', note=''), dict(id='F-9', disposition='fixed', note='')]
        notes = analysis.check_revision(value, findings)
        self.assertTrue(any('P0' in n and 'F-1' in n for n in notes))
        self.assertEqual([d['id'] for d in value['finding_dispositions']], ['F-1', 'F-2'])


class RenderTests(unittest.TestCase):
    def test_word_count(self):
        self.assertEqual(analysis_render.word_count('one two  three\n`four`'), 4)

    def test_document_order_and_blocks(self):
        record = dict(run_id='a' * 32, revision=1, parent=None, base='b' * 40, slug='follow-up-automation',
                      comments=[], calls=[dict(stage='intake', duration_seconds=10.0, metadata=dict(
                          cost=dict(status='provider-reported', usd=1.5), observed_models=['m'], usage=None))])
        journals = {'intake': dict(data=dict(brief=brief(), wish='Wish')), 'research': dict(data=dict(research=research())),
                    'analysis': dict(data=dict(analysis=analysis_output())),
                    'specification': dict(data=dict(specification=specification())),
                    'review': dict(data=dict(review=dict(review(), findings=[dict(id='F-1', severity='P1', section='API', finding='No 403', fix='Add it')]))),
                    'revision': dict(data=dict(revision(), specification=dict(
                        specification(), manifest=dict(sorted(manifest().items())))))}
        text = analysis_render.render_document(record, journals, {'analysis': ['one-pager fine']}, ahead=3, date='2026-10-25')
        self.assertIn('### Repository findings', text)
        self.assertNotIn('\n## Repository findings', text)
        self.assertTrue(text.startswith('# Follow-up automation — feature analysis\n'))
        self.assertIn('main has advanced by 3 commits', text)
        self.assertIn('cost USD 1.50', text)
        order = ['## Summary', '## Acceptance criteria', '## Data model and migrations', '## API', '## Screens',
                 '## Permissions', '## Diagrams', '## Test plan', '## Documentation to update', '## Research notes',
                 '## Review', '## Open questions', '## Rules check']
        positions = [text.index(h) for h in order]
        self.assertEqual(positions, sorted(positions))
        self.assertNotIn('## Revision notes', text)
        self.assertEqual(text.count('```json'), 2)
        self.assertIn('"check_id": "followup-rules"', text)
        block = text[text.index('```json') + len('```json\n'):]
        self.assertTrue(block.startswith('{\n  "schema_version": 1,\n  "repository": "example/repo",\n  "issue": 0,'), block[:120])
        self.assertIn('```mermaid\nstateDiagram-v2', text)
        self.assertIn('F-1', text)
        self.assertIn('fixed', text)
        record.update(revision=2, parent='c' * 32, comments=[dict(id='C-1', text='Add mail')])
        journals['analysis']['data']['analysis']['comment_dispositions'] = [dict(id='C-1', disposition='applied', note='done')]
        journals['revision']['data']['analysis']['comment_dispositions'] = [dict(id='C-1', disposition='applied', note='done')]
        text = analysis_render.render_document(record, journals, {}, ahead=None, date='2026-10-25')
        self.assertLess(text.index('## Revision notes'), text.index('## Acceptance criteria'))
        self.assertIn('C-1', text)

    def test_document_name(self):
        record = dict(slug='follow-up-automation', revision=1)
        self.assertEqual(analysis_render.document_name(record, '2026-10-25'), '2026-10-25-follow-up-automation.md')
        record = dict(slug='follow-up-automation', revision=2)
        self.assertEqual(analysis_render.document_name(record, '2026-10-26', parent_document='docs/specs/2026-10-25-follow-up-automation.md'),
                         '2026-10-25-follow-up-automation-r2.md')
        self.assertEqual(analysis_render.document_name(dict(slug='s', revision=3), '2026-10-27',
                                                       parent_document='docs/specs/2026-10-25-s-r2.md'), '2026-10-25-s-r3.md')


if __name__ == '__main__':
    unittest.main()
