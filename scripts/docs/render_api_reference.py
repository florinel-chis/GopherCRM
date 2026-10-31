#!/usr/bin/env python3
"""Render api/swagger.json as a static, self-contained HTML reference.

The output is one HTML file with inline CSS and no scripts, fonts or other
network resources, so it reads the same from file:// as from any web server.
Rendering is deterministic: the same spec always produces the same bytes.
The page leaves out info.version on purpose: the version is stamped in
exactly four places (cmd/main.go, both swagger files, gocrm-ui/package.json)
that `scripts/release/release.py prepare` bumps, and a fifth copy here would
make the release-preparation commit fail the API docs gate.
`make swagger` runs this after swag; the API docs gate regenerates and
compares the result.

Usage: render_api_reference.py SPEC_JSON OUTPUT_HTML
"""

import html
import json
import re
import sys
from pathlib import Path

METHOD_ORDER = ('get', 'post', 'put', 'patch', 'delete', 'head', 'options')
DEFINITION_PREFIXES = ('github_com_florinel-chis_gophercrm_internal_', 'internal_')
ENVELOPE_SUFFIX = 'utils.APIResponse'

CSS = """
:root{--bg:#ffffff;--fg:#1d2330;--muted:#5b6576;--line:#dde2ea;--panel:#f5f7fa;--link:#1f5fbf;
--code:#eef1f5;--get:#1b7f4b;--post:#1f5fbf;--put:#9a6200;--patch:#7a4fb3;--delete:#b3261e}
@media (prefers-color-scheme:dark){:root{--bg:#14171c;--fg:#e3e7ee;--muted:#9aa4b4;--line:#2c323c;
--panel:#1b1f26;--link:#7fb0ff;--code:#232831;--get:#4cc38a;--post:#7fb0ff;--put:#e0a93b;--patch:#b893ec;--delete:#ff8a80}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.55 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
a{color:var(--link);text-decoration:none}a:hover{text-decoration:underline}
code{background:var(--code);padding:0 .25em;border-radius:3px;font:13px/1.4 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.layout{display:flex;align-items:flex-start}
nav{position:sticky;top:0;width:290px;flex:none;height:100vh;overflow-y:auto;padding:16px;border-right:1px solid var(--line);background:var(--panel);font-size:14px}
nav ul{list-style:none;margin:0;padding:0}nav li{margin:2px 0}nav .count{color:var(--muted)}
nav details>summary{cursor:pointer;margin:2px 0}nav details ul{margin:2px 0 6px 14px}
nav details li a{display:block;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
main{flex:1;min-width:0;max-width:1100px;padding:16px 32px 64px}
h1{margin:.2em 0}h2{margin-top:2em;border-bottom:1px solid var(--line);padding-bottom:.2em}
h3{margin:1.6em 0 .4em}.muted{color:var(--muted)}
.op{border:1px solid var(--line);border-radius:6px;margin:18px 0;padding:12px 16px;background:var(--bg)}
.op h3{margin:0 0 .4em;font-size:17px;display:flex;flex-wrap:wrap;gap:.5em;align-items:baseline}
.method{display:inline-block;min-width:4.4em;text-align:center;font:700 12px/1.8 ui-monospace,Menlo,monospace;color:#fff;border-radius:4px;padding:0 .4em}
.get{background:var(--get)}.post{background:var(--post)}.put{background:var(--put)}.patch{background:var(--patch)}.delete{background:var(--delete)}
.path{font:600 15px ui-monospace,Menlo,monospace;word-break:break-all}
table{border-collapse:collapse;width:100%;margin:.4em 0 1em;font-size:14px}
th,td{border:1px solid var(--line);padding:5px 8px;text-align:left;vertical-align:top}
th{background:var(--panel);font-weight:600}
td.name{font-family:ui-monospace,Menlo,monospace;white-space:nowrap}
.req{color:var(--delete);font-size:12px;margin-left:.3em}
.facts{font-size:13px;color:var(--muted)}.facts code{color:var(--fg)}
.desc p{margin:.3em 0}.desc ul{margin:.3em 0 .6em 1.2em;padding:0}
.label{font-weight:600}
@media (max-width:860px){.layout{display:block}nav{position:static;width:auto;height:auto;border-right:0;border-bottom:1px solid var(--line)}main{padding:16px}td.name{white-space:normal;word-break:break-all}}
"""


def display_name(name):
    for prefix in DEFINITION_PREFIXES:
        if name.startswith(prefix):
            return name[len(prefix):]
    return name


def anchor(kind, text):
    return kind + '-' + re.sub(r'[^A-Za-z0-9_.-]+', '-', text).strip('-')


def esc(value):
    return html.escape(str(value), quote=True)


def inline(text):
    """Escape text and turn `spans` into code elements."""
    parts = str(text).split('`')
    out = []
    for index, part in enumerate(parts):
        if index % 2 and index != len(parts) - 1:
            out.append('<code>' + esc(part) + '</code>')
        else:
            out.append(esc(part) if index % 2 == 0 else esc('`' + part))
    return ''.join(out)


def paragraphs(text):
    """Plain description text: one paragraph per line."""
    lines = [line.strip() for line in str(text or '').split('\n')]
    return ''.join('<p>' + inline(line) + '</p>' for line in lines if line)


def prose(text):
    """Field descriptions come from wrapped Go comments: the line breaks are
    layout, not structure, so the lines join into one paragraph."""
    joined = ' '.join(line.strip() for line in str(text or '').split('\n') if line.strip())
    return '<p>' + inline(joined) + '</p>' if joined else ''


def markdown(text):
    """The small Markdown subset used by the general description:
    '## ' headings, '- ' list items and paragraphs, one per line."""
    out, in_list = [], False
    for raw in str(text or '').split('\n'):
        line = raw.strip()
        if line.startswith('- '):
            if not in_list:
                out.append('<ul>')
                in_list = True
            out.append('<li>' + inline(line[2:]) + '</li>')
            continue
        if in_list:
            out.append('</ul>')
            in_list = False
        if not line:
            continue
        if line.startswith('## '):
            heading = line[3:]
            out.append('<h3 id="' + anchor('overview', heading.lower()) + '">' + inline(heading) + '</h3>')
        else:
            out.append('<p>' + inline(line) + '</p>')
    if in_list:
        out.append('</ul>')
    return ''.join(out)


def ref_name(ref):
    return ref.rsplit('/', 1)[-1]


def ref_link(ref):
    name = ref_name(ref)
    return '<a href="#' + anchor('def', name) + '"><code>' + esc(display_name(name)) + '</code></a>'


def type_html(schema, depth=0):
    if not isinstance(schema, dict) or depth > 6:
        return 'any'
    if '$ref' in schema:
        return ref_link(schema['$ref'])
    if 'allOf' in schema:
        parts = schema['allOf']
        first = parts[0] if parts else {}
        if (len(parts) == 2 and isinstance(first, dict)
                and ref_name(first.get('$ref', '')).endswith(ENVELOPE_SUFFIX)):
            fields = ', '.join(esc(key) + ': ' + type_html(value, depth + 1)
                               for key, value in parts[1].get('properties', {}).items())
            return ref_link(first['$ref']) + (' with ' + fields if fields else '')
        return ' &amp; '.join(type_html(part, depth + 1) for part in parts)
    kind = schema.get('type')
    if kind == 'array':
        return 'array of ' + type_html(schema.get('items', {}), depth + 1)
    if kind == 'object' or (kind is None and 'properties' in schema):
        if 'properties' in schema:
            fields = ', '.join(esc(key) + ': ' + type_html(value, depth + 1)
                               for key, value in schema['properties'].items())
            return 'object {' + fields + '}'
        extra = schema.get('additionalProperties')
        if isinstance(extra, dict):
            return 'map of string to ' + type_html(extra, depth + 1)
        return 'object'
    if kind is None:
        return 'any'
    text = esc(kind)
    if schema.get('format'):
        text += ' (' + esc(schema['format']) + ')'
    return text


def facts(schema):
    """Constraints and hints of one property or parameter."""
    items = []
    target = schema
    if 'allOf' in schema and len(schema['allOf']) == 1 and isinstance(schema['allOf'][0], dict):
        target = dict(schema['allOf'][0], **{k: v for k, v in schema.items() if k != 'allOf'})
    if target.get('type') == 'array' and isinstance(target.get('items'), dict) and 'enum' in target['items']:
        items.append('items one of: ' + ', '.join('<code>' + esc(json.dumps(v)) + '</code>' for v in target['items']['enum']))
    if 'enum' in target:
        items.append('one of: ' + ', '.join('<code>' + esc(json.dumps(v)) + '</code>' for v in target['enum']))
    for key, label in (('default', 'default'), ('minimum', 'minimum'), ('maximum', 'maximum'),
                       ('minLength', 'min length'), ('maxLength', 'max length'),
                       ('minItems', 'min items'), ('maxItems', 'max items')):
        if key in target:
            items.append(label + ' <code>' + esc(json.dumps(target[key])) + '</code>')
    if target.get('readOnly'):
        items.append('read-only')
    if target.get('x-nullable'):
        items.append('nullable')
    if 'example' in target:
        items.append('example <code>' + esc(json.dumps(target['example'], ensure_ascii=False)) + '</code>')
    return '<div class="facts">' + '; '.join(items) + '</div>' if items else ''


def definition_section(name, schema):
    out = ['<section id="' + anchor('def', name) + '"><h3><code>' + esc(display_name(name)) + '</code></h3>']
    if schema.get('description'):
        out.append('<div class="desc">' + prose(schema['description']) + '</div>')
    props = schema.get('properties', {})
    required = set(schema.get('required', []))
    if props:
        out.append('<table><thead><tr><th>Field</th><th>Type</th><th>Description</th></tr></thead><tbody>')
        for key, prop in props.items():
            mark = '<span class="req">required</span>' if key in required else ''
            out.append('<tr><td class="name">' + esc(key) + mark + '</td><td>' + type_html(prop)
                       + '</td><td class="desc">' + prose(prop.get('description', '')) + facts(prop) + '</td></tr>')
        out.append('</tbody></table>')
    else:
        out.append('<p>Type: ' + type_html(schema) + '</p>' + facts(schema))
    out.append('</section>')
    return ''.join(out)


def operation_section(path, method, op, security_names):
    ident = anchor('op', method + path)
    out = ['<section class="op" id="' + ident + '"><h3><span class="method ' + esc(method) + '">'
           + esc(method.upper()) + '</span><span class="path">' + esc(path) + '</span></h3>']
    if op.get('summary'):
        out.append('<p class="label">' + inline(op['summary']) + '</p>')
    out.append('<div class="desc">' + paragraphs(op.get('description', '')) + '</div>')
    schemes = [name for requirement in op.get('security', []) for name in requirement]
    auth = ', '.join(esc(security_names.get(name, name)) for name in schemes) if schemes else 'none (public)'
    meta = ['<span class="label">Authentication:</span> ' + auth]
    if op.get('consumes'):
        meta.append('<span class="label">Request:</span> ' + ', '.join('<code>' + esc(c) + '</code>' for c in op['consumes']))
    if op.get('produces'):
        meta.append('<span class="label">Response:</span> ' + ', '.join('<code>' + esc(p) + '</code>' for p in op['produces']))
    out.append('<p class="facts">' + ' &middot; '.join(meta) + '</p>')
    params = op.get('parameters', [])
    if params:
        out.append('<table><thead><tr><th>Parameter</th><th>In</th><th>Type</th><th>Description</th></tr></thead><tbody>')
        for param in params:
            mark = '<span class="req">required</span>' if param.get('required') else ''
            kind = type_html(param['schema']) if 'schema' in param else type_html(param)
            out.append('<tr><td class="name">' + esc(param.get('name', '')) + mark + '</td><td>' + esc(param.get('in', ''))
                       + '</td><td>' + kind + '</td><td class="desc">' + paragraphs(param.get('description', ''))
                       + facts(param) + '</td></tr>')
        out.append('</tbody></table>')
    responses = op.get('responses', {})
    if responses:
        out.append('<table><thead><tr><th>Status</th><th>Body</th><th>Description</th></tr></thead><tbody>')
        for status in sorted(responses, key=lambda s: (not s.isdigit(), s)):
            response = responses[status]
            body = type_html(response['schema']) if 'schema' in response else '<span class="muted">no body</span>'
            out.append('<tr><td class="name">' + esc(status) + '</td><td>' + body + '</td><td class="desc">'
                       + paragraphs(response.get('description', '')) + '</td></tr>')
        out.append('</tbody></table>')
    out.append('</section>')
    return ''.join(out)


def render(spec):
    info = spec.get('info', {})
    title = info.get('title', 'API reference')
    tags = [t for t in spec.get('tags', []) if isinstance(t, dict) and t.get('name')]
    tag_names = [t['name'] for t in tags]
    grouped = {name: [] for name in tag_names}
    for path in sorted(spec.get('paths', {})):
        item = spec['paths'][path]
        for method in METHOD_ORDER:
            if method in item:
                op = item[method]
                tag = (op.get('tags') or ['untagged'])[0]
                if tag not in grouped:
                    grouped[tag] = []
                    tag_names.append(tag)
                grouped[tag].append((path, method, op))
    descriptions = {t['name']: t.get('description', '') for t in tags}
    security = spec.get('securityDefinitions', {})
    security_names = {name: name + ' (' + value.get('name', '') + ' header)' for name, value in security.items()}
    definitions = spec.get('definitions', {})
    def_order = sorted(definitions, key=lambda n: display_name(n).lower())
    count = sum(len(v) for v in grouped.values())

    nav = ['<nav aria-label="Contents"><p><a href="#overview"><strong>' + esc(title) + '</strong></a></p><ul>']
    for name in tag_names:
        ops = grouped[name]
        nav.append('<li><details><summary><a href="#' + anchor('tag', name) + '">' + esc(name)
                   + '</a> <span class="count">(' + str(len(ops)) + ')</span></summary><ul>')
        for path, method, op in ops:
            nav.append('<li><a href="#' + anchor('op', method + path) + '" title="' + esc(op.get('summary', ''))
                       + '">' + esc(method.upper()) + ' ' + esc(path) + '</a></li>')
        nav.append('</ul></details></li>')
    nav.append('<li><a href="#definitions">Schemas</a> <span class="count">(' + str(len(definitions)) + ')</span></li></ul></nav>')

    body = ['<main><section id="overview"><h1>' + esc(title) + '</h1>',
            '<p class="facts">Base path <code>'
            + esc(spec.get('basePath', '/')) + '</code> &middot; ' + str(count) + ' operations &middot; '
            + str(len(definitions)) + ' schemas &middot; Swagger ' + esc(spec.get('swagger', '')) + '</p>',
            '<div class="desc">' + markdown(info.get('description', '')) + '</div>']
    if security:
        body.append('<h3>Security schemes</h3><table><thead><tr><th>Name</th><th>Header</th><th>Description</th></tr></thead><tbody>')
        for name in sorted(security):
            scheme = security[name]
            body.append('<tr><td class="name">' + esc(name) + '</td><td><code>' + esc(scheme.get('name', '')) + '</code> ('
                        + esc(scheme.get('in', '')) + ')</td><td class="desc">' + paragraphs(scheme.get('description', '')) + '</td></tr>')
        body.append('</tbody></table>')
    body.append('</section>')
    for name in tag_names:
        body.append('<section id="' + anchor('tag', name) + '"><h2>' + esc(name) + '</h2>')
        if descriptions.get(name):
            body.append('<div class="desc">' + paragraphs(descriptions[name]) + '</div>')
        for path, method, op in grouped[name]:
            body.append(operation_section(path, method, op, security_names))
        body.append('</section>')
    body.append('<section id="definitions"><h2>Schemas</h2>')
    for name in def_order:
        body.append(definition_section(name, definitions[name]))
    body.append('</section></main>')

    return ''.join([
        '<!doctype html>\n<html lang="en"><head><meta charset="utf-8">',
        '<meta name="viewport" content="width=device-width, initial-scale=1">',
        '<meta name="color-scheme" content="light dark">',
        '<title>' + esc(title) + ' reference</title><style>' + CSS.strip() + '</style></head><body>',
        '<div class="layout">', ''.join(nav), '\n', '\n'.join(body), '</div></body></html>\n',
    ])


def main(argv):
    if len(argv) != 3:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    spec = json.loads(Path(argv[1]).read_text(encoding='utf-8'))
    Path(argv[2]).write_text(render(spec), encoding='utf-8', newline='\n')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
