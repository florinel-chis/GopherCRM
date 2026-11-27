"""Tests that keep the test-case catalog index (docs/testing/README.md) honest.

The README's Documents table counts each catalog file's `### TC-` cases by
priority and by the first word of their Automation bullet. These checks
recompute those numbers from the case headings and compare them with the
table, the Total row, the note about the 12-forms catalog and the total that
docs/development/forking.md quotes. They also reject wording that says an end-to-end
helper for signing in as a non-admin role is missing:
`APIClient.createUser` and `APIClient.login` in gocrm-ui/e2e/helpers/sso.ts
provide it.

Every check is a function that takes a repository root and returns a list of
problems, so it can be pointed at any checkout. The files are found through
`git ls-files`, tracked files only, as scripts/ci/check-doc-citations.py
does, so a local run and a fresh CI checkout judge the same files. Each check
is shown to report
a planted mismatch in a fixture git repository. Standard library only.
"""

import bisect
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]

README = 'docs/testing/README.md'
FORMS = 'docs/testing/12-forms.md'
SETTINGS = 'docs/testing/08-settings.md'
FORKING = 'docs/development/forking.md'
CATALOG_FILE = re.compile(r'^docs/testing/\d\d-[^/]+\.md$')

COLUMNS = ('cases', 'p0', 'p1', 'p2', 'automated', 'planned', 'blocked')
ROW = re.compile(r'^\|\s*\[(?P<name>[^\]]+\.md)\]\([^)]*\)\s*\|(?P<rest>.*)\|\s*$')
TOTAL_ROW = re.compile(r'^\|\s*\*\*Total\*\*\s*\|(?P<rest>.*)\|\s*$')
PRIORITY = re.compile(r'\*\*Priority:\*\*\s*(P[0-2])\b')
AUTOMATION = '- **Automation:**'

FORMS_ENTRY = '- **TC-FORM-'
FORMS_MARKER = re.compile(r'·\s*(automated|planned|blocked)\b')
FORMS_NOTE = re.compile(r'its (\d+) cases\b.*?(\d+) automated, (\d+) planned and (\d+) blocked')
FORKING_TOTAL = re.compile(r'manual catalog in `docs/testing/` documents (\d+) cases')

BLOCKED_HELPER = re.compile(
    r'login helper|role-login|AdminAuthHelper|provisioning helper|helper that creates a sales user',
    re.IGNORECASE)
BLOCKED_SETTINGS = re.compile(
    r'stub|no create (form|control)|no table|no (revoke|edit) control|no page consumes'
    r'|UI cannot create|no UI surface for API keys',
    re.IGNORECASE)
STALE_HELPER = re.compile(
    r'login helper'
    r'|role-login'
    r'|no e2e helper'
    r'|provisioning helper'
    r'|helper that creates a sales user'
    r'|only `AdminAuthHelper` exists'
    r'|contains only `admin-auth\.ts`'
    r'|only `(?:gocrm-ui/e2e/helpers/)?admin-auth\.ts` exists'
    r'|Building that helper'
    r'|\*?blocked\*? on that helper',
    re.IGNORECASE)


def collapse(text):
    return ' '.join(text.split())


def listed_files(root):
    """The tracked files; raises when git fails."""
    out = subprocess.run(
        ['git', '-C', str(root), 'ls-files', '-z'],
        capture_output=True, check=True).stdout.decode('utf-8')
    return sorted({name for name in out.split('\0') if name})


def read(root, name):
    return (Path(root) / name).read_text(encoding='utf-8')


def catalog_files(root):
    return [name for name in listed_files(root) if CATALOG_FILE.match(name)]


def parse_cases(text):
    """The `### TC-` cases of a catalog file, as dicts with id, line and lines."""
    cases = []
    current = None
    for number, line in enumerate(text.splitlines(), 1):
        if line.startswith('### ') or line.startswith('## '):
            current = None
            if line.startswith('### TC-'):
                current = {'id': line[4:].split()[0], 'line': number, 'lines': []}
                cases.append(current)
            continue
        if current is not None:
            current['lines'].append(line)
    return cases


def automation_bullet(lines):
    """The whitespace-collapsed Automation bullet of a case, or None."""
    for index, line in enumerate(lines):
        if line.lstrip().startswith(AUTOMATION):
            parts = [line.lstrip()[len(AUTOMATION):]]
            for follow in lines[index + 1:]:
                stripped = follow.lstrip()
                if not stripped or stripped.startswith('- **') or stripped.startswith('#'):
                    break
                parts.append(follow)
            return collapse(' '.join(parts))
    return None


def column(bullet):
    """The Documents-table column an Automation bullet counts in."""
    words = bullet.split()
    first = words[0].strip('*_').rstrip(';,:.').strip('*_').lower() if words else ''
    if first in ('planned', 'blocked'):
        return first
    return 'automated'


def case_counts(text, name='', problems=None):
    counts = dict.fromkeys(COLUMNS, 0)
    for case in parse_cases(text):
        body = '\n'.join(case['lines'])
        counts['cases'] += 1
        priority = PRIORITY.search(body)
        if priority:
            counts[priority.group(1).lower()] += 1
        elif problems is not None:
            problems.append('%s:%d: %s has no Priority' % (name, case['line'], case['id']))
        bullet = automation_bullet(case['lines'])
        if bullet is None:
            if problems is not None:
                problems.append('%s:%d: %s has no Automation bullet' % (name, case['line'], case['id']))
            continue
        counts[column(bullet)] += 1
    return counts


def numbers(rest):
    """The seven numeric cells at the end of a table row."""
    cells = [cell.strip().strip('*').strip() for cell in rest.split('|')]
    values = cells[-len(COLUMNS):]
    try:
        return dict(zip(COLUMNS, (int(value) for value in values)))
    except ValueError:
        return None


def index_rows(root):
    rows, total = {}, None
    for line in read(root, README).splitlines():
        match = ROW.match(line)
        if match:
            rows[match.group('name')] = numbers(match.group('rest'))
            continue
        match = TOTAL_ROW.match(line)
        if match:
            total = numbers(match.group('rest'))
    return rows, total


def check_index_rows(root):
    """(a) Every Documents row equals its file's counts; the Total equals their sum."""
    problems = []
    rows, total = index_rows(root)
    summed = dict.fromkeys(COLUMNS, 0)
    tracked = set(listed_files(root))
    for name, row in rows.items():
        path = 'docs/testing/' + name
        if path not in tracked:
            problems.append('%s: row links %s, which is not a tracked file' % (README, name))
            continue
        computed = case_counts(read(root, path), path, problems)
        for key in COLUMNS:
            summed[key] += computed[key]
        if row is None:
            problems.append('%s: row for %s has non-numeric cells' % (README, name))
        elif row != computed:
            problems.append('%s: row for %s reads %s, the cases give %s' % (
                README, name, [row[key] for key in COLUMNS], [computed[key] for key in COLUMNS]))
    if total is None:
        problems.append('%s: no Total row' % README)
    elif total != summed:
        problems.append('%s: Total reads %s, the rows sum to %s' % (
            README, [total[key] for key in COLUMNS], [summed[key] for key in COLUMNS]))
    return problems


def check_every_file_has_row(root):
    """(b) Every docs/testing/NN-*.md except 12-forms.md has a Documents row."""
    rows, _ = index_rows(root)
    return ['%s: no Documents row for %s' % (README, name) for name in catalog_files(root)
            if name != FORMS and name[len('docs/testing/'):] not in rows]


def forms_entries(text):
    entries, current = [], None
    for number, line in enumerate(text.splitlines(), 1):
        if line.startswith(FORMS_ENTRY):
            current = {'line': number, 'lines': [line]}
            entries.append(current)
        elif line.startswith('#'):
            current = None
        elif current is not None:
            current['lines'].append(line)
    return entries


def check_forms_note(root):
    """(c) Every TC-FORM entry has one marker, and the README note states their tally."""
    problems = []
    tally = {'automated': 0, 'planned': 0, 'blocked': 0}
    entries = forms_entries(read(root, FORMS))
    for entry in entries:
        markers = FORMS_MARKER.findall(collapse(' '.join(entry['lines'])))
        if len(markers) != 1:
            problems.append('%s:%d: entry has %d automation markers, expected 1' % (
                FORMS, entry['line'], len(markers)))
            continue
        tally[markers[0]] += 1
    note = FORMS_NOTE.search(collapse(read(root, README)))
    expected = (len(entries), tally['automated'], tally['planned'], tally['blocked'])
    if note is None:
        problems.append('%s: no note stating the 12-forms counts' % README)
    elif tuple(int(value) for value in note.groups()) != expected:
        problems.append('%s: the 12-forms note states %s cases/automated/planned/blocked, the entries give %s'
                        % (README, '/'.join(note.groups()), '/'.join(str(value) for value in expected)))
    return problems


def blocked_bullets(root, names):
    for name in names:
        for case in parse_cases(read(root, name)):
            bullet = automation_bullet(case['lines'])
            if bullet is not None and column(bullet) == 'blocked':
                yield name, case, bullet


def check_blocked_not_on_helper(root):
    """(d) No blocked case names a missing sign-in helper as its blocker."""
    names = [name for name in catalog_files(root) if name != FORMS]
    return ['%s:%d: %s is blocked on a helper: %s' % (name, case['line'], case['id'], match.group(0))
            for name, case, bullet in blocked_bullets(root, names)
            for match in [BLOCKED_HELPER.search(bullet)] if match]


def check_settings_blocked_on_page(root):
    """(e) No 08-settings case is blocked on a missing API Keys page control."""
    return ['%s:%d: %s is blocked on the page: %s' % (name, case['line'], case['id'], match.group(0))
            for name, case, bullet in blocked_bullets(root, [SETTINGS])
            for match in [BLOCKED_SETTINGS.search(bullet)] if match]


def check_forking_total(root):
    """(f) docs/development/forking.md quotes the catalog's Total cases."""
    _, total = index_rows(root)
    match = FORKING_TOTAL.search(collapse(read(root, FORKING)))
    if match is None:
        return ['%s: no "manual catalog in `docs/testing/` documents N cases" sentence' % FORKING]
    if total is None or int(match.group(1)) != total['cases']:
        return ['%s: documents %s cases, the README Total is %s' % (
            FORKING, match.group(1), None if total is None else total['cases'])]
    return []


def collapsed_with_lines(text):
    """The whitespace-collapsed text, the start offset of each word and its line."""
    words, starts, lines = [], [], []
    offset = 0
    for match in re.finditer(r'\S+', text):
        starts.append(offset)
        lines.append(text.count('\n', 0, match.start()) + 1)
        words.append(match.group(0))
        offset += len(match.group(0)) + 1
    return ' '.join(words), starts, lines


def check_stale_helper_wording(root):
    """(g) No catalog file says a sign-in helper for other roles is missing."""
    problems = []
    for name in listed_files(root):
        if not (name.startswith('docs/testing/') and name.endswith('.md') and name.count('/') == 2):
            continue
        text, starts, lines = collapsed_with_lines(read(root, name))
        for match in STALE_HELPER.finditer(text):
            line = lines[bisect.bisect_right(starts, match.start()) - 1]
            problems.append('%s:%d: %s' % (name, line, match.group(0)))
    return problems


class CatalogIndexTest(unittest.TestCase):
    """The checks pass on this repository."""

    def assert_clean(self, check):
        problems = check(ROOT)
        self.assertEqual(problems, [], '\n'.join(problems))

    def test_index_rows_match_cases(self):
        self.assert_clean(check_index_rows)

    def test_every_catalog_file_has_row(self):
        self.assert_clean(check_every_file_has_row)

    def test_forms_note_matches_entries(self):
        self.assert_clean(check_forms_note)

    def test_blocked_cases_not_on_helper(self):
        self.assert_clean(check_blocked_not_on_helper)

    def test_settings_cases_not_blocked_on_page(self):
        self.assert_clean(check_settings_blocked_on_page)

    def test_forking_total_matches(self):
        self.assert_clean(check_forking_total)

    def test_no_stale_helper_wording(self):
        self.assert_clean(check_stale_helper_wording)


def case(case_id, priority='P0', automation='automated — `gocrm-ui/e2e/tests/a.spec.ts` "x"'):
    return ('### %s — Title\n- **Priority:** %s\n- **Type:** functional\n'
            '- **Expected:** It works.\n- **Automation:** %s\n\n' % (case_id, priority, automation))


ONE = '# One\n\n## Section\n\n' + case('TC-ONE-001') + case('TC-ONE-002', 'P1', 'planned — `x.spec.ts`')
SETTINGS_DOC = '# Settings\n\n' + case('TC-SET-001', 'P2', 'blocked — waits out a real expiry')
FORMS_DOC = ('# Forms\n\n## 1\n\n- **TC-FORM-001 — a** · automated · `x_test.go`\n'
             '- **TC-FORM-002 — b** (wrapped\n  over two lines) · planned · `y`\n')
README_DOC = """# Catalog

| Document | Area | Cases | P0 | P1 | P2 | Automated | Planned | Blocked |
|---|---|---|---|---|---|---|---|---|
| [01-one.md](01-one.md) | One | 2 | 1 | 1 | 0 | 1 | 1 | 0 |
| [08-settings.md](08-settings.md) | Settings | 1 | 0 | 0 | 1 | 0 | 0 | 1 |
| **Total** | | **3** | **1** | **1** | **1** | **1** | **1** | **1** |

(`12-forms.md` is not in this table: its 2 cases are entries without priorities, 1 automated, 1
planned and 0 blocked.)
"""
FORKING_DOC = 'The\nmanual catalog in `docs/testing/` documents 3 cases.\n'


class CatalogFixtureTest(unittest.TestCase):
    """Each check reports a mismatch planted in a fixture repository."""

    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.root = Path(folder.name).resolve()
        subprocess.run(['git', 'init', '-q'], cwd=self.root, check=True, capture_output=True)
        self.write('docs/testing/README.md', README_DOC)
        self.write('docs/testing/01-one.md', ONE)
        self.write('docs/testing/08-settings.md', SETTINGS_DOC)
        self.write('docs/testing/12-forms.md', FORMS_DOC)
        self.write(FORKING, FORKING_DOC)

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding='utf-8')
        subprocess.run(['git', 'add', '--', path], cwd=self.root, check=True, capture_output=True)

    def replace(self, path, old, new):
        text = (self.root / path).read_text(encoding='utf-8')
        self.assertIn(old, text)
        self.write(path, text.replace(old, new, 1))

    def assert_reports(self, check, fragment):
        problems = check(self.root)
        self.assertTrue(any(fragment in problem for problem in problems), problems)

    def test_fixture_is_clean(self):
        for check in (check_index_rows, check_every_file_has_row, check_forms_note,
                      check_blocked_not_on_helper, check_settings_blocked_on_page,
                      check_forking_total, check_stale_helper_wording):
            self.assertEqual(check(self.root), [], check.__name__)

    def test_wrong_case_count(self):
        self.replace(README, '| One | 2 |', '| One | 3 |')
        self.assert_reports(check_index_rows, 'row for 01-one.md')

    def test_wrong_priority_column(self):
        self.replace(README, '| One | 2 | 1 | 1 | 0 |', '| One | 2 | 2 | 0 | 0 |')
        self.assert_reports(check_index_rows, 'row for 01-one.md')

    def test_wrong_automation_column(self):
        self.replace(README, '| 0 | 1 | 1 | 0 |\n| [08', '| 0 | 2 | 0 | 0 |\n| [08')
        self.assert_reports(check_index_rows, 'row for 01-one.md')

    def test_wrong_total(self):
        self.replace(README, '| **Total** | | **3** |', '| **Total** | | **4** |')
        self.assert_reports(check_index_rows, 'Total reads')

    def test_missing_row(self):
        self.write('docs/testing/02-two.md', '# Two\n\n' + case('TC-TWO-001'))
        self.assert_reports(check_every_file_has_row, 'no Documents row for docs/testing/02-two.md')

    def test_row_links_untracked_file(self):
        target = self.root / 'docs/testing/02-two.md'
        target.write_text('# Two\n\n' + case('TC-TWO-001'), encoding='utf-8')
        self.replace(README, '| [08-settings.md]',
                     '| [02-two.md](02-two.md) | Two | 1 | 1 | 0 | 0 | 1 | 0 | 0 |\n| [08-settings.md]')
        self.assert_reports(check_index_rows, 'row links 02-two.md, which is not a tracked file')
        self.assertEqual(check_every_file_has_row(self.root), [])

    def test_forms_entry_without_marker(self):
        self.replace(FORMS, '· automated · `x_test.go`', '`x_test.go`')
        self.assert_reports(check_forms_note, 'has 0 automation markers')

    def test_forms_entry_with_two_markers(self):
        self.replace(FORMS, '· automated · `x_test.go`', '· automated · `x_test.go` · planned ·')
        self.assert_reports(check_forms_note, 'has 2 automation markers')

    def test_wrong_forms_note(self):
        self.replace(README, 'its 2 cases', 'its 5 cases')
        self.assert_reports(check_forms_note, 'the 12-forms note states 5/1/1/0')

    def test_wrong_forking_total(self):
        self.replace(FORKING, 'documents 3 cases', 'documents 4 cases')
        self.assert_reports(check_forking_total, 'documents 4 cases, the README Total is 3')

    def test_blocked_on_wrapped_helper(self):
        self.write('docs/testing/01-one.md', ONE + case(
            'TC-ONE-003', 'P2', 'blocked — needs the login\n  helper for sales'))
        self.replace(README, '| One | 2 | 1 | 1 | 0 | 1 | 1 | 0 |', '| One | 3 | 1 | 1 | 1 | 1 | 1 | 1 |')
        self.replace(README, '| **Total** | | **3** | **1** | **1** | **1** | **1** | **1** | **1** |',
                     '| **Total** | | **4** | **1** | **1** | **2** | **1** | **1** | **2** |')
        self.replace(FORKING, 'documents 3 cases', 'documents 4 cases')
        self.assertEqual(check_index_rows(self.root), [])
        self.assert_reports(check_blocked_not_on_helper, 'TC-ONE-003 is blocked on a helper: login helper')

    def test_settings_blocked_on_missing_table(self):
        self.replace(SETTINGS, 'waits out a real expiry', 'the page has no table')
        self.assert_reports(check_settings_blocked_on_page, 'TC-SET-001 is blocked on the page: no table')

    def test_planned_with_semicolon_counts_as_planned(self):
        self.replace('docs/testing/01-one.md', 'planned — `x.spec.ts`', '*planned;* `x.spec.ts`')
        self.assertEqual(check_index_rows(self.root), [])
        self.assertEqual(column('*planned;* `x.spec.ts`'), 'planned')
        self.assertEqual(column('blocked in the browser — clock'), 'blocked')
        self.assertEqual(column('`internal/x_test.go` `TestX`'), 'automated')

    def test_stale_phrase_wrapped_in_constraint(self):
        self.replace('docs/testing/01-one.md', '## Section\n',
                     '## Section\n\nSales cases wait for the login\nhelper.\n')
        self.assertEqual(check_stale_helper_wording(self.root),
                         ['docs/testing/01-one.md:5: login helper'])

    def test_stale_phrase_across_lines_reports_first_line(self):
        self.replace('docs/testing/01-one.md', '## Section\n', '## Section\n\nThe provisioning\nhelper is missing.\n')
        self.assertEqual(check_stale_helper_wording(self.root),
                         ['docs/testing/01-one.md:5: provisioning helper'])


if __name__ == '__main__':
    unittest.main()
