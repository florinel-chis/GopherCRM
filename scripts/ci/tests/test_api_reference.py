"""Tests for the static HTML API reference renderer (scripts/docs/render_api_reference.py)."""

import importlib.util
import json
from pathlib import Path
import re
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
loader = importlib.util.spec_from_file_location('render_api_reference', ROOT / 'scripts' / 'docs' / 'render_api_reference.py')
renderer = importlib.util.module_from_spec(loader)
loader.loader.exec_module(renderer)

ENVELOPE = 'github_com_florinel-chis_gophercrm_internal_utils.APIResponse'


def sample():
    return {
        'swagger': '2.0',
        'info': {'title': 'Test API', 'version': '9.9.9',
                 'description': 'Intro with `code`.\n\n## Authentication\nSend a token.\n- first\n- second <b>'},
        'basePath': '/api/v1',
        'securityDefinitions': {'BearerAuth': {'type': 'apiKey', 'in': 'header', 'name': 'Authorization',
                                               'description': 'Bearer <jwt>'}},
        'tags': [{'name': 'widgets', 'description': 'Widgets & parts.'}],
        'paths': {'/widgets/{id}': {'get': {
            'summary': 'Get <one>', 'description': 'Line one.\nLine two <script>alert(1)</script>',
            'tags': ['widgets'], 'security': [{'BearerAuth': []}], 'produces': ['application/json'],
            'parameters': [{'name': 'id', 'in': 'path', 'required': True, 'type': 'integer', 'description': 'Id'}],
            'responses': {
                '200': {'description': 'OK', 'schema': {'allOf': [
                    {'$ref': '#/definitions/' + ENVELOPE},
                    {'type': 'object', 'properties': {'data': {'type': 'array', 'items': {
                        '$ref': '#/definitions/models.Widget'}}}}]}},
                '204': {'description': 'Nothing'},
            }}}},
        'definitions': {
            ENVELOPE: {'type': 'object', 'properties': {'data': {'description': 'Payload'}}},
            'models.Widget': {'type': 'object', 'description': 'A widget', 'required': ['name'], 'properties': {
                'name': {'type': 'string', 'description': 'Wrapped\ncomment text', 'maxLength': 20,
                         'example': 'Gear'},
                'colour': {'type': 'string', 'description': 'Colour', 'enum': ['red', 'blue'], 'x-nullable': True},
            }},
        },
    }


class RendererTests(unittest.TestCase):
    def test_deterministic(self):
        self.assertEqual(renderer.render(sample()), renderer.render(json.loads(json.dumps(sample()))))

    def test_version_is_not_rendered(self):
        # release.py prepare bumps the version in the swagger files only; the
        # page must stay byte-identical so the release commit passes the gate.
        bumped = sample()
        bumped['info']['version'] = '9.9.10'
        self.assertEqual(renderer.render(sample()), renderer.render(bumped))
        self.assertNotIn('9.9.9', renderer.render(sample()))

    def test_self_contained_and_offline(self):
        page = renderer.render(sample())
        self.assertNotRegex(page, r'<script|<link|<img|<iframe|\ssrc=|@import|url\(')
        self.assertNotRegex(page, r'(?:href|src)="(?:https?:)?//')

    def test_escapes_spec_text(self):
        page = renderer.render(sample())
        self.assertNotIn('<script>alert(1)</script>', page)
        self.assertIn('&lt;script&gt;alert(1)&lt;/script&gt;', page)
        self.assertIn('Get &lt;one&gt;', page)
        self.assertIn('Widgets &amp; parts.', page)

    def test_general_description_markdown_subset(self):
        page = renderer.render(sample())
        self.assertIn('<code>code</code>', page)
        self.assertIn('<h3 id="overview-authentication">Authentication</h3>', page)
        self.assertIn('<ul><li>first</li><li>second &lt;b&gt;</li></ul>', page)

    def test_operations_definitions_and_links(self):
        page = renderer.render(sample())
        self.assertIn('id="op-get-widgets-id"', page)
        self.assertIn('href="#op-get-widgets-id"', page)
        self.assertIn('id="def-models.Widget"', page)
        self.assertIn('array of <a href="#def-models.Widget"><code>models.Widget</code></a>', page)
        self.assertIn('<code>utils.APIResponse</code></a> with data:', page)
        self.assertIn('no body', page)
        self.assertIn('BearerAuth (Authorization header)', page)

    def test_property_facts_and_wrapped_descriptions(self):
        page = renderer.render(sample())
        self.assertIn('<p>Wrapped comment text</p>', page)
        self.assertIn('max length <code>20</code>', page)
        self.assertIn('example <code>&quot;Gear&quot;</code>', page)
        self.assertIn('one of: <code>&quot;red&quot;</code>, <code>&quot;blue&quot;</code>', page)
        self.assertIn('nullable', page)
        self.assertRegex(page, r'name<span class="req">required</span>')

    def test_every_operation_and_definition_is_rendered(self):
        spec = json.loads((ROOT / 'api' / 'swagger.json').read_text())
        page = renderer.render(spec)
        operations = sum(1 for item in spec['paths'].values() for m in item if m in renderer.METHOD_ORDER)
        self.assertEqual(len(re.findall(r'<section class="op" id="op-', page)), operations)
        self.assertEqual(len(re.findall(r'<section id="def-', page)), len(spec['definitions']))

    def test_committed_reference_matches_the_committed_spec(self):
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder) / 'index.html'
            self.assertEqual(renderer.main(['render', str(ROOT / 'api' / 'swagger.json'), str(out)]), 0)
            self.assertEqual(out.read_bytes(), (ROOT / 'api' / 'index.html').read_bytes(),
                             'api/index.html is stale; run make swagger')


if __name__ == '__main__':
    unittest.main()
