"""Markdown rendering for the feature analysis document. Pure functions over stage data."""

import json
import re


def word_count(text: str) -> int:
    return len(re.findall(r"[A-Za-z0-9_'`/.-]+", text))


def bullets(items, empty='- (none)') -> list[str]:
    return [f'- {item}' for item in items] or [empty]


def section(title: str, lines: list[str]) -> list[str]:
    return ['', f'## {title}', '', *lines]


def render_brief(brief: dict, wish: str) -> str:
    lines = [f'# Brief: {brief["title"]}', '', f'Slug: `{brief["slug"]}`']
    if wish:
        lines += ['', '## Request', '', wish.strip()]
    lines += ['', '## Restated in project terms', '', brief['restatement'], '', '## Project terms', '']
    for term in brief['project_terms']:
        paths = ', '.join(f'`{p}`' for p in term['paths']) or '(no path)'
        lines.append(f'- **{term["term"]}**: {term["meaning"]} ({paths})')
    for heading, key in (('In scope', 'in_scope'), ('Out of scope', 'out_of_scope'),
                         ('Assumptions', 'assumptions'), ('Open questions', 'open_questions')):
        lines += section(heading, bullets(brief[key]))
    return '\n'.join(lines) + '\n'


def render_research(research: dict) -> str:
    lines = ['# Research', '', research['summary']]
    lines += section('Repository findings', bullets(
        f'{f["claim"]} (`{f["file"]}:{f["line"]}`: `{f.get("text", "")}`)' for f in research['repository_findings']))
    lines += section('Conventions that bind the design', bullets(
        f'{f["constraint"]} (`{f["file"]}:{f["line"]}`)' for f in research['conventions']))
    lines += section('External findings (reported by the agent)', bullets(
        f'{f["claim"]} — {f["title"]} <{f["url"]}>' for f in research['external_findings']))
    lines += section('Prior art', bullets(
        f'**{p["name"]}** <{p["url"]}>: {p["summary"]} Relevance: {p["relevance"]}' for p in research['prior_art']))
    lines += section('Gaps', bullets(research['gaps']))
    return '\n'.join(lines) + '\n'


def render_one_pager(analysis: dict) -> str:
    lines = ['## Summary', '', f'**Problem.** {analysis["problem"]}', '',
             '**Users.** ' + '; '.join(f'{u["role"]}: {u["need"]}' for u in analysis['users']), '',
             f'**Proposal.** {analysis["proposal"]}', '',
             '**In scope.** ' + '; '.join(analysis['in_scope']), '',
             '**Out of scope.** ' + '; '.join(analysis['out_of_scope']), '',
             '**Main flow.**']
    lines += [f'{n}. {step}' for n, step in enumerate(analysis['main_flow'], 1)]
    lines += ['', '**Options considered.** ' + '; '.join(f'{o["option"]} (not chosen: {o["why_not"]})'
                                                        for o in analysis['options_considered']), '',
              '**Risks.** ' + '; '.join(f'{r["risk"]} (mitigation: {r["mitigation"]})' for r in analysis['risks']), '',
              f'**Size.** {analysis["size"]["t_shirt"]}: {analysis["size"]["basis"]}', '',
              '**Decision needed.** ' + '; '.join(analysis['decision_needed'])]
    return '\n'.join(lines) + '\n'


def render_revision_notes(comments: list[dict], dispositions: dict) -> list[str]:
    lines = []
    for comment in comments:
        disposition = dispositions.get(comment['id'])
        verdict = f'{disposition["disposition"]}: {disposition["note"]}' if disposition else 'no disposition recorded'
        lines.append(f'- **{comment["id"]}** {comment["text"]}  \n  {verdict}')
    return lines or ['- (no comments)']


def fenced(language: str, body: str) -> list[str]:
    return [f'```{language}', body.rstrip('\n'), '```']


def render_specification(spec: dict) -> list[str]:
    from .analysis import registry
    manifest = spec['manifest']
    lines = section('Acceptance criteria', [])
    for criterion in manifest['acceptance']:
        lines.append(f'- **{criterion["id"]}** {criterion["requirement"]}  \n  named check `{criterion["verification"]["check_id"]}`')
    for criterion in spec['ui_criteria']:
        v = criterion['verification']
        lines.append(f'- **{criterion["id"]}** {criterion["requirement"]}  \n  verified by {v["kind"]}: {v["detail"]} (frontend slice, outside the runner manifest)')
    lines += ['', 'Manifest (`manifest.json`; set `issue` before `freeze --manifest`):', '']
    lines += fenced('json', json.dumps(manifest, indent=2))
    lines += ['', 'Check registry to merge into the private policy (`acceptance_checks.json`):', '']
    lines += fenced('json', json.dumps({'acceptance_checks': registry(spec)}, indent=2))
    model = spec['data_model']
    body = []
    for entity in model['entities']:
        body.append(f'**{entity["name"]}** (`{entity["table"]}`)')
        body += [f'- `{c["name"]}` {c["type"]}{", nullable" if c["nullable"] else ""}: {c["note"]}' for c in entity['columns']]
        if entity['indexes']:
            body.append('- indexes: ' + ', '.join(f'`{i}`' for i in entity['indexes']))
        if entity['relations']:
            body.append('- relations: ' + ', '.join(entity['relations']))
        body.append('')
    body += ['AutoMigrate changes:', *bullets(f'`{m}`' for m in model['auto_migrate']), '',
             f'SQL migration `{model["sql_migration"]["name"]}` up:', *fenced('sql', '\n'.join(model['sql_migration']['up'])), '',
             'down:', *fenced('sql', '\n'.join(model['sql_migration']['down']))]
    lines += section('Data model and migrations', body)
    lines += section('API', bullets(
        f'`{a["method"]} {a["path"]}` roles {", ".join(a["roles"])}; request: {a["request"]}; response: {a["response"]}; errors: {", ".join(a["errors"])}'
        for a in spec['api']))
    lines += section('Screens', bullets(
        f'`{s["route"]}` **{s["page"]}**: {s["purpose"]} ({", ".join(s["elements"])})' for s in spec['screens']))
    lines += section('Permissions', bullets(f'**{p["role"]}**: {", ".join(p["actions"])}' for p in spec['permissions']))
    diagrams = []
    for diagram in spec['diagrams']:
        diagrams += [f'### {diagram["title"]} ({diagram["kind"]})', '', *fenced('mermaid', diagram['mermaid']), '']
    lines += section('Diagrams', diagrams or ['(none)'])
    lines += section('Test plan', bullets(f'{t["level"]}: `{t["name"]}` proves {t["proves"]}' for t in spec['test_plan']))
    lines += section('Documentation to update', bullets(
        f'`{d["path"]}` ({d.get("status", "unchecked")}): {d["change"]}' for d in spec['docs_to_update']))
    return lines


def render_review(review: dict | None, dispositions: list[dict]) -> list[str]:
    if review is None:
        return ['(no review recorded)']
    by_id = {d['id']: d for d in dispositions}
    lines = [f'Verdict before revision: **{review["verdict"]}**. {review["summary"]}', '']
    for finding in review['findings']:
        disposition = by_id.get(finding['id'])
        outcome = f'{disposition["disposition"]}: {disposition["note"]}' if disposition else 'no disposition'
        lines.append(f'- **{finding["id"]}** ({finding["severity"]}, {finding["section"]}) {finding["finding"]}  \n'
                     f'  fix: {finding["fix"]}  \n  revision: {outcome}')
    if not review['findings']:
        lines.append('- (no findings)')
    lines += ['', 'Criteria assessment:', '']
    lines += [f'- {c["id"]}: observable {"yes" if c["observable"] else "no"}, verifiable {"yes" if c["verifiable"] else "no"}'
              + (f' — {c["note"]}' if c['note'] else '') for c in review['criteria']]
    return lines


def cost_summary(calls: list[dict]) -> dict:
    known = [c['metadata']['cost']['usd'] for c in calls
             if (c.get('metadata') or {}).get('cost', {}).get('status') == 'provider-reported']
    status = 'provider-reported' if calls and len(known) == len(calls) else ('partial' if known else 'unavailable')
    models = sorted({m for c in calls for m in ((c.get('metadata') or {}).get('observed_models') or [])})
    duration = sum(c.get('duration_seconds') or 0 for c in calls)
    return {'status': status, 'usd': sum(known) if known else None, 'duration_seconds': duration,
            'models': models, 'calls': len(calls)}


def render_document(record: dict, journals: dict, notes_by_stage: dict, *, ahead, date: str) -> str:
    brief = journals['intake']['data']['brief']
    research = journals['research']['data']['research']
    if 'revision' in journals:
        final = journals['revision']['data']
    else:
        final = {'analysis': journals['analysis']['data']['analysis'],
                 'specification': journals['specification']['data']['specification'], 'finding_dispositions': []}
    analysis, spec = final['analysis'], final['specification']
    review = (journals.get('review') or {}).get('data', {}).get('review')
    summary = cost_summary(record.get('calls', []))
    minutes, seconds = divmod(int(summary['duration_seconds']), 60)
    header = [f'# {brief["title"]} — feature analysis', '']
    run_line = f'Run {record["run_id"]} · revision {record["revision"]}'
    if record.get('parent'):
        run_line += f' · parent {record["parent"]}'
    run_line += f' · GopherCRM main @ {record["base"]}'
    if ahead:
        run_line += f' · main has advanced by {ahead} commits'
    run_line += f' · {date}'
    cost = f'USD {summary["usd"]:.2f} ({summary["status"]})' if summary['usd'] is not None else summary['status']
    header += [run_line,
               f'Agent calls: {summary["calls"]} · cost {cost} · duration {minutes:02d}:{seconds:02d} · '
               f'models observed: {", ".join(summary["models"]) or "unknown"}']
    severity = {f['id']: f['severity'] for f in (review or {}).get('findings', [])}
    unresolved = [d['id'] for d in final['finding_dispositions']
                  if d['disposition'] == 'declined' and severity.get(d['id']) in {'P0', 'P1'}]
    if unresolved:
        header.append(f'Unresolved: declined findings {", ".join(unresolved)}')
    lines = header + ['', render_one_pager(analysis).rstrip('\n')]
    if record['revision'] > 1:
        dispositions = {d['id']: d for d in analysis['comment_dispositions']}
        dispositions.update({d['id']: d for d in spec['comment_dispositions']})
        lines += section('Revision notes', render_revision_notes(record.get('comments', []), dispositions))
    lines += render_specification(spec)
    lines += section('Research notes', render_research(research).split('\n', 2)[2].strip('\n').splitlines())
    lines += section('Review', render_review(review, final['finding_dispositions']))
    lines += section('Open questions', bullets([*brief['open_questions'], *spec['open_questions']]))
    rules = []
    for stage, notes in notes_by_stage.items():
        rules += [f'- {stage}: {note}' for note in notes]
    lines += section('Rules check', rules or ['- all deterministic checks passed'])
    return '\n'.join(lines) + '\n'


def document_name(record: dict, date: str, parent_document: str | None = None) -> str:
    if record['revision'] == 1 or not parent_document:
        return f'{date}-{record["slug"]}.md'
    stem = parent_document.rsplit('/', 1)[-1].removesuffix('.md')
    stem = re.sub(r'-r[0-9]+$', '', stem)
    return f'{stem}-r{record["revision"]}.md'
