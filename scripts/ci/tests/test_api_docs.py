"""Mutation tests for the API documentation gate (scripts/ci/check-api-docs.py).

Each rule is shown to pass on a small valid spec and to fail on the smallest
change that breaks it.
"""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
spec_loader = importlib.util.spec_from_file_location('check_api_docs', ROOT / 'scripts' / 'ci' / 'check-api-docs.py')
gate = importlib.util.module_from_spec(spec_loader)
spec_loader.loader.exec_module(gate)

ENVELOPE = 'github_com_florinel-chis_gophercrm_internal_utils.APIResponse'
ERROR = 'github_com_florinel-chis_gophercrm_internal_utils.APIError'


def error_response(text='NOT_FOUND: no such widget'):
    return {'description': text, 'schema': {'allOf': [
        {'$ref': '#/definitions/' + ENVELOPE},
        {'type': 'object', 'properties': {'error': {'$ref': '#/definitions/' + ERROR}}}]}}


def valid_spec():
    return {
        'swagger': '2.0',
        'info': {'title': 'Test API', 'version': '1.0.0', 'description': 'Intro'},
        'basePath': '/api/v1',
        'securityDefinitions': {
            'BearerAuth': {'type': 'apiKey', 'in': 'header', 'name': 'Authorization',
                           'description': 'Send "Authorization: Bearer <jwt>".'},
            'ApiKeyAuth': {'type': 'apiKey', 'in': 'header', 'name': 'Authorization',
                           'description': 'Send "Authorization: ApiKey <key>".'},
        },
        'tags': [{'name': 'widgets', 'description': 'Widgets.'}],
        'paths': {
            '/widgets/{id}': {'get': {
                'summary': 'Get a widget', 'description': 'One widget.', 'tags': ['widgets'],
                'security': [{'BearerAuth': []}, {'ApiKeyAuth': []}],
                'parameters': [{'name': 'id', 'in': 'path', 'required': True, 'type': 'integer',
                                'description': 'Widget id'},
                               {'name': 'fields', 'in': 'query', 'type': 'string', 'description': 'Fields to return'}],
                'responses': {
                    '200': {'description': 'The widget', 'schema': {'allOf': [
                        {'$ref': '#/definitions/' + ENVELOPE},
                        {'type': 'object', 'properties': {'data': {'$ref': '#/definitions/models.Widget'}}}]}},
                    '401': error_response('UNAUTHORIZED: no credentials'),
                    '404': error_response(),
                },
            }},
            '/widgets': {'post': {
                'summary': 'Create a widget', 'description': 'New widget.', 'tags': ['widgets'],
                'security': [{'BearerAuth': []}, {'ApiKeyAuth': []}],
                'parameters': [{'name': 'request', 'in': 'body', 'required': True, 'description': 'Widget',
                                'schema': {'$ref': '#/definitions/models.Widget'}}],
                'responses': {
                    '201': {'description': 'Created'},
                    '400': error_response('VALIDATION_ERROR: bad widget'),
                    '401': error_response('UNAUTHORIZED: no credentials'),
                },
            }},
            '/widgets/confirm': {'post': {
                'summary': 'Confirm', 'description': 'Public confirmation page.', 'tags': ['widgets'],
                'consumes': ['application/x-www-form-urlencoded', 'application/json'],
                'parameters': [{'name': 'token', 'in': 'formData', 'type': 'string', 'description': 'Token'}],
                'responses': {'200': {'description': 'HTML page'}, '429': error_response('TOO_MANY_REQUESTS')},
            }},
        },
        'definitions': {
            ENVELOPE: {'type': 'object', 'description': 'Envelope', 'properties': {
                'success': {'type': 'boolean', 'description': 'Outcome'},
                'data': {'description': 'Payload'},
                'error': {'description': 'Failure', 'allOf': [{'$ref': '#/definitions/' + ERROR}]},
            }},
            ERROR: {'type': 'object', 'properties': {
                'code': {'type': 'string', 'description': 'Code'},
                'details': {'type': 'object', 'description': 'Details'},
            }},
            'models.WidgetColour': {'type': 'string', 'enum': ['red', 'blue']},
            'models.Widget': {'type': 'object', 'properties': {
                'id': {'type': 'integer', 'description': 'Id'},
                'colour': {'description': 'Colour', 'allOf': [{'$ref': '#/definitions/models.WidgetColour'}]},
                'size': {'type': 'string', 'description': 'Size', 'enum': ['', 's', 'm']},
                'parts': {'type': 'array', 'description': 'Parts', 'items': {
                    'type': 'object', 'properties': {'name': {'type': 'string', 'description': 'Part name'}}}},
            }},
        },
    }


ROUTES = [
    {'method': 'GET', 'path': '/widgets/{id}', 'base': True, 'auth': True, 'responses': [401]},
    {'method': 'POST', 'path': '/widgets', 'base': True, 'auth': True, 'responses': [401]},
    {'method': 'POST', 'path': '/widgets/confirm', 'base': True, 'auth': False, 'responses': [429]},
    {'method': 'GET', 'path': '/health', 'base': False, 'auth': False, 'responses': []},
]
INPUTS = [
    {'method': 'GET', 'path': '/widgets/{id}', 'query': ['fields'], 'form': [], 'body': False, 'file': 'h.go', 'line': 1},
    {'method': 'POST', 'path': '/widgets', 'query': [], 'form': [], 'body': True, 'body_types': ['models.Widget'],
     'body_required': True, 'file': 'h.go', 'line': 2},
    # Reads the form field and falls back to a JSON body: Swagger 2.0 cannot
    # declare both, so the documented form field suffices.
    {'method': 'POST', 'path': '/widgets/confirm', 'query': [], 'form': ['token'], 'body': True,
     'body_types': ['handler.confirmRequest'], 'body_required': False, 'file': 'h.go', 'line': 3},
]
SETS = {'WidgetColour': ['red', 'blue'], 'WidgetSize': ['s', 'm']}
EXPECTATIONS = [
    ('models.WidgetColour', None, 'WidgetColour', False),
    ('models.Widget', 'colour', 'WidgetColour', False),
    ('models.Widget', 'size', 'WidgetSize', True),
]


def problems(spec, routes=ROUTES, inputs=INPUTS, base_path='/api/v1'):
    with patch.object(gate, 'ENUM_EXPECTATIONS', EXPECTATIONS):
        return gate.check_spec(spec, routes, SETS, inputs, base_path)


class SpecRuleTests(unittest.TestCase):
    def assertFails(self, spec, rule, routes=ROUTES, inputs=INPUTS, base_path='/api/v1'):
        found = problems(spec, routes, inputs, base_path)
        self.assertTrue(any(p.startswith(rule + ':') for p in found), f'{rule} not reported: {found}')

    def test_valid_spec_passes(self):
        self.assertEqual(problems(valid_spec()), [])

    def mutated(self, change):
        spec = valid_spec()
        change(spec)
        return spec

    def op(self, spec):
        return spec['paths']['/widgets/{id}']['get']

    def create(self, spec):
        return spec['paths']['/widgets']['post']

    # base path
    def test_wrong_base_path(self):
        self.assertFails(self.mutated(lambda s: s.update(basePath='/wrong')), 'base-path')

    def test_base_path_follows_the_configured_prefix(self):
        self.assertFails(valid_spec(), 'base-path', base_path='/api/v2')

    def test_repository_base_path_default(self):
        self.assertEqual(gate.configured_base_path(ROOT), '/api/v1')

    # inputs
    def test_query_parameter_read_but_not_documented(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['parameters'].pop(1)), 'inputs')

    def test_query_parameter_documented_but_not_read(self):
        inputs = [dict(INPUTS[0], query=[])] + INPUTS[1:]
        self.assertFails(valid_spec(), 'inputs', inputs=inputs)

    def test_bound_body_not_documented(self):
        self.assertFails(self.mutated(lambda s: self.create(s).update(parameters=[])), 'inputs')

    def test_documented_body_not_bound(self):
        inputs = [INPUTS[0], dict(INPUTS[1], body=False), INPUTS[2]]
        self.assertFails(valid_spec(), 'inputs', inputs=inputs)

    def test_body_schema_of_another_type(self):
        self.assertFails(self.mutated(lambda s: self.create(s)['parameters'][0].update(
            schema={'type': 'string'})), 'inputs')

    def test_body_schema_names_another_definition(self):
        self.assertFails(self.mutated(lambda s: self.create(s)['parameters'][0].update(
            schema={'$ref': '#/definitions/' + ERROR})), 'inputs')

    def test_required_body_documented_as_optional(self):
        self.assertFails(self.mutated(lambda s: self.create(s)['parameters'][0].update(required=False)), 'inputs')

    def test_optional_body_documented_as_required(self):
        inputs = [INPUTS[0], dict(INPUTS[1], body_required=False), INPUTS[2]]
        self.assertFails(valid_spec(), 'inputs', inputs=inputs)

    def test_bound_body_without_400(self):
        self.assertFails(self.mutated(lambda s: self.create(s)['responses'].pop('400')), 'inputs')

    def test_two_body_parameters(self):
        def change(s):
            params = self.create(s)['parameters']
            params.append(dict(params[0], name='again'))
        self.assertFails(self.mutated(change), 'inputs')

    def test_form_field_not_documented(self):
        def change(s):
            s['paths']['/widgets/confirm']['post']['parameters'] = []
        self.assertFails(self.mutated(change), 'inputs')

    def test_annotated_operation_missing_from_spec(self):
        inputs = INPUTS + [{'method': 'GET', 'path': '/gadgets', 'query': [], 'form': [], 'body': False,
                            'file': 'h.go', 'line': 9}]
        self.assertFails(valid_spec(), 'inputs', inputs=inputs)

    # route security
    def test_protected_route_documented_as_public(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(security=[])), 'routes')

    def test_protected_route_with_one_scheme(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(security=[{'BearerAuth': []}])), 'routes')

    def test_anonymous_security_alternative(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['security'].append({})), 'routes')

    def test_combined_security_requirement(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(
            security=[{'BearerAuth': [], 'ApiKeyAuth': []}])), 'routes')

    def test_security_requirement_with_scopes(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(
            security=[{'BearerAuth': ['admin']}, {'ApiKeyAuth': []}])), 'routes')

    def test_global_security_requirement(self):
        self.assertFails(self.mutated(lambda s: s.update(security=[{'BearerAuth': []}])), 'routes')

    def test_middleware_response_not_documented(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['responses'].pop('401')), 'routes')

    def test_rate_limit_response_not_documented(self):
        self.assertFails(self.mutated(lambda s: s['paths']['/widgets/confirm']['post']['responses'].update(
            {'400': s['paths']['/widgets/confirm']['post']['responses'].pop('429')})), 'routes')

    def test_public_route_documented_as_protected(self):
        def change(s):
            s['paths']['/widgets/confirm']['post']['security'] = [{'BearerAuth': []}, {'ApiKeyAuth': []}]
        self.assertFails(self.mutated(change), 'routes')

    # routes
    def test_routed_endpoint_without_operation(self):
        self.assertFails(valid_spec(), 'routes', ROUTES + [{'method': 'POST', 'path': '/widgets', 'base': True}])

    def test_operation_without_route(self):
        self.assertFails(valid_spec(), 'routes', [r for r in ROUTES if r['base'] is False])

    def test_route_outside_base_path_needs_exclusion(self):
        self.assertFails(valid_spec(), 'routes', ROUTES + [{'method': 'GET', 'path': '/metrics', 'base': False}])

    def test_stale_exclusion(self):
        self.assertFails(valid_spec(), 'routes', [r for r in ROUTES if r['base']])

    def test_method_mismatch(self):
        self.assertFails(valid_spec(), 'routes', [{'method': 'PUT', 'path': '/widgets/{id}', 'base': True}, ROUTES[1]])

    # operations
    def test_operation_without_description(self):
        self.assertFails(self.mutated(lambda s: self.op(s).pop('description')), 'operations')

    def test_operation_without_summary(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(summary=' ')), 'operations')

    def test_operation_with_two_tags(self):
        def change(s):
            s['tags'].append({'name': 'extra', 'description': 'More.'})
            self.op(s)['tags'] = ['widgets', 'extra']
        self.assertFails(self.mutated(change), 'operations')

    def test_operation_with_undeclared_tag(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(tags=['gadgets'])), 'operations')

    def test_parameter_without_description(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['parameters'][0].pop('description')), 'operations')

    def test_operation_without_error_response(self):
        def change(s):
            for code in ('401', '404'):
                self.op(s)['responses'].pop(code)
        self.assertFails(self.mutated(change), 'operations')

    def test_operation_without_success_response(self):
        self.assertFails(self.mutated(lambda s: self.create(s)['responses'].pop('201')), 'operations')

    def test_response_without_description(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['responses']['404'].update(description='')), 'operations')

    # tags
    def test_tag_without_description(self):
        self.assertFails(self.mutated(lambda s: s['tags'][0].pop('description')), 'tags')

    def test_unused_tag(self):
        self.assertFails(self.mutated(lambda s: s['tags'].append({'name': 'idle', 'description': 'x'})), 'tags')

    def test_duplicate_tag(self):
        self.assertFails(self.mutated(lambda s: s['tags'].append(dict(s['tags'][0]))), 'tags')

    # properties
    def test_definition_property_without_description(self):
        self.assertFails(self.mutated(lambda s: s['definitions']['models.Widget']['properties']['id'].pop('description')),
                         'properties')

    def test_bare_ref_property_has_no_description(self):
        def change(s):
            s['definitions']['models.Widget']['properties']['colour'] = {'$ref': '#/definitions/models.WidgetColour'}
        self.assertFails(self.mutated(change), 'properties')

    def test_nested_array_item_property_without_description(self):
        def change(s):
            s['definitions']['models.Widget']['properties']['parts']['items']['properties']['name'].pop('description')
        self.assertFails(self.mutated(change), 'properties')

    def test_inline_response_property_without_description(self):
        def change(s):
            self.op(s)['responses']['200']['schema']['allOf'][1]['properties']['data'] = {
                'type': 'object', 'properties': {'items': {'type': 'array', 'items': {'type': 'string'}}}}
        self.assertFails(self.mutated(change), 'properties')

    def test_override_of_undescribed_base_property_is_reported(self):
        def change(s):
            s['definitions'][ENVELOPE]['properties']['data'].pop('description')
        self.assertFails(self.mutated(change), 'properties')

    def test_nested_override_exempt_when_base_describes_it(self):
        def change(s):
            self.op(s)['responses']['404']['schema']['allOf'][1]['properties']['error'] = {'allOf': [
                {'$ref': '#/definitions/' + ERROR},
                {'type': 'object', 'properties': {'details': {'$ref': '#/definitions/models.Widget'}}}]}
        self.assertEqual(problems(self.mutated(change)), [])

    def test_override_of_property_missing_from_base_is_reported(self):
        def change(s):
            self.op(s)['responses']['404']['schema']['allOf'][1]['properties']['extra'] = {'type': 'string'}
        self.assertFails(self.mutated(change), 'properties')

    # refs
    def test_unresolved_ref(self):
        def change(s):
            self.op(s)['responses']['200']['schema']['allOf'][1]['properties']['data'] = {
                '$ref': '#/definitions/models.Missing'}
        self.assertFails(self.mutated(change), 'refs')

    # path parameters
    def test_path_parameter_missing(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(parameters=[])), 'path-params')

    def test_path_parameter_not_in_path(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['parameters'][0].update(name='widget_id')), 'path-params')

    def test_path_parameter_not_required(self):
        self.assertFails(self.mutated(lambda s: self.op(s)['parameters'][0].update(required=False)), 'path-params')

    # security
    def test_api_key_scheme_must_use_the_authorization_header(self):
        # The defect fixed in #227: a header literally named ApiKey is never read.
        self.assertFails(self.mutated(lambda s: s['securityDefinitions']['ApiKeyAuth'].update(name='ApiKey')),
                         'security')

    def test_undeclared_security_scheme(self):
        self.assertFails(self.mutated(lambda s: self.op(s).update(security=[{'CookieAuth': []}])), 'security')

    def test_security_scheme_without_description(self):
        self.assertFails(self.mutated(lambda s: s['securityDefinitions']['BearerAuth'].pop('description')), 'security')

    def test_api_key_description_must_show_the_authorization_form(self):
        # #227 again, in prose: a description telling clients to send an ApiKey header.
        self.assertFails(self.mutated(lambda s: s['securityDefinitions']['ApiKeyAuth'].update(
            description='Send ApiKey: <key>')), 'security')

    def test_bearer_description_must_show_the_authorization_form(self):
        self.assertFails(self.mutated(lambda s: s['securityDefinitions']['BearerAuth'].update(
            description='A JWT access token.')), 'security')

    def test_security_scheme_missing(self):
        self.assertFails(self.mutated(lambda s: s['securityDefinitions'].pop('ApiKeyAuth')), 'security')

    def test_unknown_security_scheme_declared(self):
        self.assertFails(self.mutated(lambda s: s['securityDefinitions'].update(CookieAuth={
            'type': 'apiKey', 'in': 'header', 'name': 'Authorization', 'description': 'Authorization: Cookie <x>'})),
            'security')

    # enums
    def test_enum_missing(self):
        self.assertFails(self.mutated(lambda s: s['definitions']['models.WidgetColour'].pop('enum')), 'enums')

    def test_enum_values_differ(self):
        self.assertFails(self.mutated(
            lambda s: s['definitions']['models.WidgetColour'].update(enum=['red', 'green'])), 'enums')

    def test_enum_extra_value(self):
        self.assertFails(self.mutated(
            lambda s: s['definitions']['models.WidgetColour'].update(enum=['red', 'blue', 'green'])), 'enums')

    def test_enum_duplicate_value(self):
        self.assertFails(self.mutated(
            lambda s: s['definitions']['models.WidgetColour'].update(enum=['red', 'blue', 'red'])), 'enums')

    def test_empty_value_only_where_allowed(self):
        self.assertFails(self.mutated(
            lambda s: s['definitions']['models.Widget']['properties']['size'].update(enum=['s', 'm'])), 'enums')

    def test_expected_property_missing(self):
        self.assertFails(self.mutated(lambda s: s['definitions']['models.Widget']['properties'].pop('size')), 'enums')

    def test_expected_definition_missing(self):
        self.assertFails(self.mutated(lambda s: s['definitions'].pop('models.WidgetColour')), 'enums')

    def test_closed_set_without_values(self):
        with patch.object(gate, 'ENUM_EXPECTATIONS', EXPECTATIONS):
            found = gate.check_spec(valid_spec(), ROUTES, dict(SETS, WidgetSize=[]))
        self.assertTrue(any(p.startswith('enums:') for p in found), found)

    def test_enum_through_array_items(self):
        def change(s):
            s['definitions']['models.Widget']['properties']['colour'] = {
                'type': 'array', 'description': 'Colours', 'items': {'type': 'string', 'enum': ['blue', 'red']}}
        self.assertEqual(problems(self.mutated(change)), [])

    def test_ref_cycle_does_not_loop(self):
        def change(s):
            s['definitions']['models.WidgetColour'] = {'$ref': '#/definitions/models.WidgetColour'}
        self.assertFails(self.mutated(change), 'enums')


class ConstantTests(unittest.TestCase):
    def test_typed_and_prefixed_constants(self):
        with tempfile.TemporaryDirectory() as folder:
            models = Path(folder) / 'internal' / 'models'
            models.mkdir(parents=True)
            (models / 'widget.go').write_text(
                'package models\n\ntype WidgetColour string\n\nconst (\n'
                '\tWidgetColourRed  WidgetColour = "red"\n\tWidgetColourBlue WidgetColour = "blue"\n)\n\n'
                'const (\n\tWidgetShapeRound = "round"\n\tWidgetShapeMaxSides = 12\n)\n')
            (models / 'widget_test.go').write_text('package models\n\nconst WidgetShapeFake = "fake"\n')
            constants = gate.go_constants(folder)
        with patch.object(gate, 'CLOSED_SETS', {
                'Colour': [('type', 'WidgetColour')], 'Shape': [('prefix', 'WidgetShape')],
                'Fixed': [('values', ['a'])], 'Both': [('prefix', 'WidgetShape'), ('values', ['b'])],
                'Settable': [('type', 'WidgetColour'), ('exclude', ['blue'])]}):
            sets = gate.closed_sets(constants)
        self.assertEqual(sets, {'Colour': ['red', 'blue'], 'Shape': ['round'], 'Fixed': ['a'],
                                'Both': ['round', 'b'], 'Settable': ['red']})

    def test_lead_create_and_bulk_status_exclude_converted(self):
        sets = gate.closed_sets(gate.go_constants(ROOT))
        self.assertIn('converted', sets['LeadStatus'])
        self.assertEqual(sorted(sets['LeadStatusSettable']), ['contacted', 'new', 'qualified', 'unqualified'])

    def test_every_expectation_names_a_known_set(self):
        for definition, _, set_name, _ in gate.ENUM_EXPECTATIONS:
            self.assertIn(set_name, gate.CLOSED_SETS, definition)

    def test_repository_sets_resolve_to_values(self):
        sets = gate.closed_sets(gate.go_constants(ROOT))
        for name, values in sets.items():
            self.assertTrue(values, name)
        self.assertEqual(sorted(sets['UserRole']), ['admin', 'customer', 'sales', 'support'])


class ArtifactTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.api = Path(self.folder.name) / 'api'
        self.fresh = Path(self.folder.name) / 'fresh'
        for directory in (self.api, self.fresh):
            directory.mkdir()
            for name in gate.ARTIFACTS:
                (directory / name).write_text('same ' + name)

    def test_identical_artifacts_pass(self):
        self.assertEqual(gate.check_artifacts(self.api, self.fresh), [])

    def test_stale_artifact(self):
        (self.api / 'index.html').write_text('old')
        self.assertTrue(any(p.startswith('regenerate:') for p in gate.check_artifacts(self.api, self.fresh)))

    def test_missing_artifact(self):
        (self.api / 'swagger.yaml').unlink()
        self.assertTrue(any(p.startswith('artifacts:') for p in gate.check_artifacts(self.api, self.fresh)))

    def test_unexpected_artifact(self):
        (self.api / 'docs.go').write_text('package docs')
        self.assertTrue(any(p.startswith('artifacts:') for p in gate.check_artifacts(self.api, self.fresh)))

    def test_generator_did_not_produce_an_artifact(self):
        (self.fresh / 'swagger.json').unlink()
        self.assertTrue(any(p.startswith('regenerate:') for p in gate.check_artifacts(self.api, self.fresh)))

    def test_size_guard(self):
        with patch.object(gate, 'MAX_ARTIFACT_BYTES', 4):
            self.assertTrue(any(p.startswith('size:') for p in gate.check_artifacts(self.api)))


class MainTests(unittest.TestCase):
    def run_main(self, generated):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'api').mkdir()
            spec = valid_spec()
            (root / 'api' / 'swagger.json').write_text(json.dumps(spec))
            self.assertEqual(problems(spec), [])
            for name in ('swagger.yaml', 'index.html'):
                (root / 'api' / name).write_text(name)

            def generate(_, out_dir):
                for name in gate.ARTIFACTS:
                    (Path(out_dir) / name).write_bytes(generated.get(name, (root / 'api' / name).read_bytes()))

            with patch.object(gate, 'extract_source', return_value=(ROUTES, INPUTS)), \
                    patch.object(gate, 'configured_base_path', return_value='/api/v1'), \
                    patch.object(gate, 'generate', side_effect=generate), \
                    patch.object(gate, 'go_constants', return_value=[]), \
                    patch.object(gate, 'CLOSED_SETS', {name: [('values', v)] for name, v in SETS.items()}), \
                    patch.object(gate, 'ENUM_EXPECTATIONS', EXPECTATIONS), \
                    patch('sys.stderr'), patch('sys.stdout'):
                return gate.main(['--root', folder])

    def test_current_artifacts_pass(self):
        self.assertEqual(self.run_main({}), 0)

    def test_regeneration_drift_fails(self):
        self.assertEqual(self.run_main({'index.html': b'newer'}), 1)


class RepositorySpecTests(unittest.TestCase):
    """The committed spec satisfies every rule that needs no Go toolchain."""

    def test_committed_spec(self):
        spec = json.loads((ROOT / 'api' / 'swagger.json').read_text())
        # Route authentication is taken from the spec itself here; the full gate
        # reads it from the router source.
        documented = [{'method': m.upper(), 'path': p, 'base': True, 'auth': bool(op.get('security'))}
                      for p, m, op in gate.operations(spec)]
        routes = documented + [{'method': 'GET', 'path': '/health', 'base': False, 'auth': False}]
        found = gate.check_spec(spec, routes, gate.closed_sets(gate.go_constants(ROOT)), (),
                                gate.configured_base_path(ROOT))
        self.assertEqual(found, [])


if __name__ == '__main__':
    unittest.main()
