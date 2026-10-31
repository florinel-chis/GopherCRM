#!/usr/bin/env python3
"""API documentation gate (`make verify-api-docs`).

Fails when the committed API reference is stale or incomplete:

  artifacts    api/ holds exactly swagger.json, swagger.yaml and index.html.
  regenerate   a fresh `make api-docs` into a temporary directory reproduces
               every artifact byte for byte.
  size         index.html stays within the tracked-file size guard.
  base-path    basePath equals the API_PREFIX default in internal/config.
  routes       every route the router registers (read from source by
               scripts/ci/apiroutes) has an operation and every operation a
               route; GET /health outside the base path is the one exclusion.
               An operation behind middleware.Auth declares exactly the two
               alternatives [{BearerAuth: []}, {ApiKeyAuth: []}] (no anonymous
               {} alternative, no combined requirement); a public one declares
               none, and the spec has no global security. Every status code
               the route middleware can answer with (401 Auth, 403 role and
               registration guards, 429 rate limiters) is a documented
               response.
  inputs       read from the handler the router registers for the route
               (which must carry the route's @Router annotation; an @Router
               on any other function fails): every query parameter and form
               field it reads (helpers included) is a documented parameter
               and every documented one is read; a handler that binds a body
               documents exactly one body parameter whose schema is a $ref to
               the bound Go type, required exactly when a missing body is
               refused, plus a 400 response; only such a handler documents a
               body (an operation that documents form fields is exempt from
               the body rules, since Swagger 2.0 cannot declare both).
  operations   summary, description, exactly one declared tag, described
               parameters and responses, at least one 2xx/3xx and one 4xx/5xx
               response.
  tags         declared once, described, and used.
  properties   every property of every definition and of every inline schema
               has a description (an allOf override of a property its base
               definition describes, such as the envelope's data, excepted).
  refs         every $ref resolves.
  path-params  path templates and in:path parameters match both ways.
  security     exactly the BearerAuth and ApiKeyAuth schemes, both on the
               Authorization header, each description naming its
               "Authorization: <scheme> <credential>" form; operations
               reference declared schemes only.
  enums        the known closed sets carry exactly the values of their Go
               constants (or the fixed lists below).

Semantic accuracy of the prose is a review responsibility, not this gate's.
"""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ARTIFACTS = ('swagger.json', 'swagger.yaml', 'index.html')
MAX_ARTIFACT_BYTES = 5 * 1024 * 1024  # scripts/ci/check-file-sizes.sh
METHODS = ('get', 'post', 'put', 'patch', 'delete', 'head', 'options')
DEFINITION_PREFIXES = ('github_com_florinel-chis_gophercrm_internal_', 'internal_')
# Routes outside the API base path that the reference deliberately omits.
EXCLUDED_ROUTES = {('GET', '/health')}
SECURITY_HEADER = 'Authorization'
# Security scheme -> the Authorization scheme word middleware.Auth accepts.
SECURITY_SCHEMES = {'BearerAuth': 'Bearer', 'ApiKeyAuth': 'ApiKey'}
BASE_PATH_SOURCE = Path('internal') / 'config' / 'config.go'
BASE_PATH_PATTERN = re.compile(r'getEnv\("API_PREFIX",\s*"([^"]*)"\)')

# Closed sets. ('type', T) collects the string constants of Go type T,
# ('prefix', P) the untyped string constants whose name starts with P, both
# from internal/models; ('values', [...]) is a fixed list for sets the code
# spells out without constants (the source is named in the comment);
# ('exclude', [...]) drops values the collected constants include.
CLOSED_SETS = {
    'UserRole': [('type', 'UserRole')],
    'LeadStatus': [('type', 'LeadStatus')],
    # lead create and bulk status refuse converted (internal/handler/lead_handler.go,
    # internal/service/bulk_status_service.go): only the conversion sets it.
    'LeadStatusSettable': [('type', 'LeadStatus'), ('exclude', ['converted'])],
    'LeadClassification': [('type', 'LeadClassification')],
    'TaskStatus': [('type', 'TaskStatus')],
    'TaskPriority': [('type', 'TaskPriority')],
    'TicketStatus': [('type', 'TicketStatus')],
    'TicketPriority': [('type', 'TicketPriority')],
    'DealStage': [('type', 'DealStage')],
    'QuoteStatus': [('type', 'QuoteStatus')],
    'FormStatus': [('type', 'FormStatus')],
    'FormSubmissionStatus': [('type', 'FormSubmissionStatus')],
    'ConfigurationType': [('type', 'ConfigurationType')],
    'ConfigurationCategory': [('type', 'ConfigurationCategory')],
    'FormFieldType': [('prefix', 'FormField')],
    'FormSubmitAction': [('prefix', 'FormSubmitAction')],
    'FormSpamReason': [('prefix', 'FormSpamReason')],
    'AEOTrigger': [('prefix', 'AEOTrigger')],
    'AEORunStatus': [('prefix', 'AEORunStatus')],
    'AutomationEntity': [('prefix', 'AutomationEntity')],
    'AutomationTrigger': [('prefix', 'AutomationEvent'), ('prefix', 'AutomationTrigger')],
    'AutomationStepType': [('prefix', 'AutomationStep')],
    'AutomationRunStatus': [('prefix', 'AutomationRun')],
    'AutomationCancelReason': [('prefix', 'AutomationCancel')],
    'AutomationOutcome': [('prefix', 'AutomationOutcome')],
    'AutomationRecipient': [('prefix', 'AutomationRecipient')],
    'AutomationElse': [('prefix', 'AutomationElse')],
    # binding oneof on CompanyRequest.EmployeeRange (internal/handler/company_handler.go)
    'EmployeeRange': [('values', ['1-10', '11-50', '51-200', '201-500', '501-1000', '1000+'])],
    # activity literals in internal/handler/dashboard_handler.go
    'ActivityType': [('values', ['lead_created', 'lead_converted', 'ticket_created', 'ticket_resolved',
                                 'task_completed'])],
}

# (definition, property or None for an enum definition, set, also allows "").
ENUM_EXPECTATIONS = [
    ('models.UserRole', None, 'UserRole', False),
    ('models.LeadStatus', None, 'LeadStatus', False),
    ('models.LeadClassification', None, 'LeadClassification', False),
    ('models.TaskStatus', None, 'TaskStatus', False),
    ('models.TaskPriority', None, 'TaskPriority', False),
    ('models.TicketStatus', None, 'TicketStatus', False),
    ('models.TicketPriority', None, 'TicketPriority', False),
    ('models.DealStage', None, 'DealStage', False),
    ('models.QuoteStatus', None, 'QuoteStatus', False),
    ('models.FormStatus', None, 'FormStatus', False),
    ('models.FormSubmissionStatus', None, 'FormSubmissionStatus', False),
    ('models.ConfigurationType', None, 'ConfigurationType', False),
    ('models.ConfigurationCategory', None, 'ConfigurationCategory', False),
    ('models.User', 'role', 'UserRole', False),
    ('handler.CreateUserRequest', 'role', 'UserRole', False),
    ('handler.UpdateUserRequest', 'role', 'UserRole', False),
    ('models.Lead', 'status', 'LeadStatus', False),
    ('models.Lead', 'classification', 'LeadClassification', False),
    ('handler.CreateLeadRequest', 'status', 'LeadStatusSettable', False),
    ('handler.CreateLeadRequest', 'classification', 'LeadClassification', False),
    ('handler.UpdateLeadRequest', 'status', 'LeadStatus', False),
    ('handler.UpdateLeadRequest', 'classification', 'LeadClassification', False),
    ('handler.BulkLeadStatusRequest', 'status', 'LeadStatusSettable', False),
    ('models.Task', 'status', 'TaskStatus', False),
    ('models.Task', 'priority', 'TaskPriority', False),
    ('handler.CreateTaskRequest', 'priority', 'TaskPriority', False),
    ('handler.UpdateTaskRequest', 'status', 'TaskStatus', False),
    ('handler.UpdateTaskRequest', 'priority', 'TaskPriority', False),
    ('handler.BulkTaskStatusRequest', 'status', 'TaskStatus', False),
    ('models.Ticket', 'status', 'TicketStatus', False),
    ('models.Ticket', 'priority', 'TicketPriority', False),
    ('handler.CreateTicketRequest', 'priority', 'TicketPriority', False),
    ('handler.UpdateTicketRequest', 'status', 'TicketStatus', False),
    ('handler.UpdateTicketRequest', 'priority', 'TicketPriority', False),
    ('handler.BulkTicketStatusRequest', 'status', 'TicketStatus', False),
    ('models.Deal', 'stage', 'DealStage', False),
    ('handler.DealRequest', 'stage', 'DealStage', False),
    ('handler.DealStageRequest', 'stage', 'DealStage', False),
    ('models.DealStageChange', 'from_stage', 'DealStage', False),
    ('models.DealStageChange', 'to_stage', 'DealStage', False),
    ('models.DealPipelineStage', 'stage', 'DealStage', False),
    ('models.QuoteDealSummary', 'stage', 'DealStage', False),
    ('models.QuoteJSON', 'status', 'QuoteStatus', False),
    ('models.QuoteStatusChangeJSON', 'from_status', 'QuoteStatus', False),
    ('models.QuoteStatusChangeJSON', 'to_status', 'QuoteStatus', False),
    ('handler.QuoteStatusRequest', 'status', 'QuoteStatus', False),
    ('models.Company', 'employee_range', 'EmployeeRange', True),
    ('handler.CompanyRequest', 'employee_range', 'EmployeeRange', False),
    ('handler.ConfigurationResponse', 'type', 'ConfigurationType', False),
    ('handler.ConfigurationResponse', 'category', 'ConfigurationCategory', False),
    ('handler.Activity', 'type', 'ActivityType', False),
    ('models.Form', 'status', 'FormStatus', False),
    ('models.Form', 'submit_action', 'FormSubmitAction', False),
    ('handler.CreateFormRequest', 'status', 'FormStatus', False),
    ('handler.CreateFormRequest', 'submit_action', 'FormSubmitAction', False),
    ('handler.UpdateFormRequest', 'status', 'FormStatus', False),
    ('handler.UpdateFormRequest', 'submit_action', 'FormSubmitAction', False),
    ('handler.FormListItem', 'status', 'FormStatus', False),
    ('handler.FormListItem', 'submit_action', 'FormSubmitAction', False),
    ('models.FormFieldDef', 'type', 'FormFieldType', False),
    ('models.FormSubmission', 'status', 'FormSubmissionStatus', False),
    ('models.FormSubmission', 'spam_reason', 'FormSpamReason', True),
    ('service.PublicFormDefinition', 'submit_action', 'FormSubmitAction', False),
    ('service.SubmitOutcome', 'action', 'FormSubmitAction', False),
    ('models.AEORun', 'trigger', 'AEOTrigger', False),
    ('models.AEORun', 'status', 'AEORunStatus', False),
    ('models.AutomationRule', 'entity_type', 'AutomationEntity', False),
    ('models.AutomationRule', 'trigger_type', 'AutomationTrigger', False),
    ('handler.AutomationRuleRequest', 'entity_type', 'AutomationEntity', False),
    ('handler.AutomationRuleRequest', 'trigger_type', 'AutomationTrigger', False),
    ('handler.AutomationDryRunRequest', 'entity_type', 'AutomationEntity', False),
    ('handler.AutomationDraftDryRunRequest', 'entity_type', 'AutomationEntity', False),
    ('models.AutomationDryRunReport', 'entity_type', 'AutomationEntity', False),
    ('models.AutomationRun', 'entity_type', 'AutomationEntity', False),
    ('models.Notification', 'entity_type', 'AutomationEntity', False),
    ('models.AutomationStep', 'step_type', 'AutomationStepType', False),
    ('handler.AutomationStepRequest', 'step_type', 'AutomationStepType', False),
    ('models.AutomationRunStep', 'step_type', 'AutomationStepType', False),
    ('models.AutomationDryRunStep', 'step_type', 'AutomationStepType', False),
    ('models.AutomationRun', 'status', 'AutomationRunStatus', False),
    ('models.AutomationRun', 'cancel_reason', 'AutomationCancelReason', False),
    ('models.AutomationRunStep', 'outcome', 'AutomationOutcome', False),
    ('models.AutomationStepConfig', 'recipient', 'AutomationRecipient', False),
    ('models.AutomationStepConfig', 'else', 'AutomationElse', False),
    ('models.AutomationStepConfig', 'priority', 'TaskPriority', False),
    ('models.AutomationScheduleFilter', 'status', 'LeadStatus', False),
    ('models.AutomationScheduleFilter', 'stage', 'DealStage', False),
]

CONST_PATTERN = re.compile(r'^\s*([A-Z]\w*)\s+(?:([A-Z]\w*)\s+)?=\s*"((?:[^"\\]|\\.)*)"', re.MULTILINE)


def display_name(name):
    for prefix in DEFINITION_PREFIXES:
        if name.startswith(prefix):
            return name[len(prefix):]
    return name


def go_constants(root):
    """(name, type or None, value) for every string constant in internal/models."""
    constants = []
    for path in sorted((Path(root) / 'internal' / 'models').glob('*.go')):
        if path.name.endswith('_test.go'):
            continue
        for match in CONST_PATTERN.finditer(path.read_text(encoding='utf-8')):
            constants.append((match.group(1), match.group(2), json.loads('"' + match.group(3) + '"')))
    return constants


def closed_sets(constants):
    sets = {}
    for name, sources in CLOSED_SETS.items():
        values = []
        excluded = set()
        for kind, key in sources:
            if kind == 'values':
                values.extend(key)
            if kind == 'exclude':
                excluded.update(key)
                continue
            for const, typ, value in constants:
                if (kind == 'type' and typ == key) or (kind == 'prefix' and typ is None and const.startswith(key)):
                    values.append(value)
        sets[name] = [v for v in values if v not in excluded]
    return sets


def resolve(spec, ref):
    if not isinstance(ref, str) or not ref.startswith('#/definitions/'):
        return None
    return spec.get('definitions', {}).get(ref[len('#/definitions/'):])


def enum_of(spec, schema, seen=frozenset()):
    """The enum a schema carries directly or through $ref, allOf or array items."""
    if not isinstance(schema, dict):
        return None
    if 'enum' in schema:
        return schema['enum']
    if '$ref' in schema:
        if schema['$ref'] in seen:
            return None
        return enum_of(spec, resolve(spec, schema['$ref']), seen | {schema['$ref']})
    for part in schema.get('allOf', []):
        found = enum_of(spec, part, seen)
        if found is not None:
            return found
    if schema.get('type') == 'array':
        return enum_of(spec, schema.get('items'), seen)
    return None


def operations(spec):
    for path, item in spec.get('paths', {}).items():
        for method in METHODS:
            if isinstance(item, dict) and method in item:
                yield path, method, item[method]


def described(node):
    return isinstance(node, dict) and isinstance(node.get('description'), str) and node['description'].strip() != ''


def walk_properties(spec, schema, where, problems, depth=0):
    """Require descriptions on the properties of an inline or definition schema.

    swag writes `X{field=Y}` as allOf [$ref X, {properties: {field: Y}}]. Such
    an override property may stay bare when X itself describes `field`.
    """
    if not isinstance(schema, dict) or depth > 32:
        return
    parts = schema.get('allOf', [])
    base = {}
    for part in parts:
        if isinstance(part, dict) and '$ref' in part:
            base.update((resolve(spec, part['$ref']) or {}).get('properties') or {})
    for index, part in enumerate(parts):
        if not isinstance(part, dict) or '$ref' in part:
            continue
        for name, prop in (part.get('properties') or {}).items():
            if not described(prop) and not described(base.get(name)):
                problems.append(f'properties: {where} allOf[{index}].{name} has no description')
            walk_properties(spec, prop, f'{where}.{name}', problems, depth + 1)
        for key in ('items', 'additionalProperties', 'allOf'):
            if key in part:
                walk_properties(spec, {key: part[key]}, f'{where} allOf[{index}]', problems, depth + 1)
    for name, prop in (schema.get('properties') or {}).items():
        if not described(prop):
            problems.append(f'properties: {where}.{name} has no description')
        walk_properties(spec, prop, f'{where}.{name}', problems, depth + 1)
    for key in ('items', 'additionalProperties'):
        if isinstance(schema.get(key), dict):
            walk_properties(spec, schema[key], f'{where}[{key}]', problems, depth + 1)


def check_tags(spec):
    problems = []
    declared = {}
    for tag in spec.get('tags', []):
        name = tag.get('name') if isinstance(tag, dict) else None
        if not name:
            problems.append('tags: a declared tag has no name')
            continue
        if name in declared:
            problems.append(f'tags: {name} is declared twice')
        declared[name] = tag
        if not described(tag):
            problems.append(f'tags: {name} has no description')
    used = {t for _, _, op in operations(spec) for t in op.get('tags', [])}
    for name in sorted(set(declared) - used):
        problems.append(f'tags: {name} is declared but no operation uses it')
    return problems


def check_operations(spec):
    problems = []
    declared = {t.get('name') for t in spec.get('tags', []) if isinstance(t, dict)}
    for path, method, op in operations(spec):
        where = f'{method.upper()} {path}'
        if not str(op.get('summary', '')).strip():
            problems.append(f'operations: {where} has no summary')
        if not described(op):
            problems.append(f'operations: {where} has no description')
        tags = op.get('tags', [])
        if len(tags) != 1:
            problems.append(f'operations: {where} has {len(tags)} tags, expected exactly one')
        for tag in tags:
            if tag not in declared:
                problems.append(f'operations: {where} uses undeclared tag {tag}')
        for param in op.get('parameters', []):
            if not described(param):
                problems.append(f'operations: {where} parameter {param.get("name")} has no description')
            if 'schema' in param:
                walk_properties(spec, param['schema'], f'{where} body', problems)
        responses = op.get('responses', {})
        if not responses:
            problems.append(f'operations: {where} has no responses')
        if not any(str(code)[:1] in '23' for code in responses):
            problems.append(f'operations: {where} documents no 2xx/3xx response')
        if not any(str(code)[:1] in '45' for code in responses):
            problems.append(f'operations: {where} documents no 4xx/5xx response')
        for code, response in responses.items():
            if not described(response):
                problems.append(f'operations: {where} response {code} has no description')
            if isinstance(response, dict) and 'schema' in response:
                walk_properties(spec, response['schema'], f'{where} response {code}', problems)
    return problems


def check_properties(spec):
    problems = []
    for name, schema in spec.get('definitions', {}).items():
        walk_properties(spec, schema, display_name(name), problems)
    return problems


def check_refs(spec):
    problems = []

    def walk(node, where):
        if isinstance(node, dict):
            if '$ref' in node and resolve(spec, node['$ref']) is None:
                problems.append(f'refs: {where} points at missing {node["$ref"]}')
            for key, value in node.items():
                walk(value, f'{where}/{key}')
        elif isinstance(node, list):
            for index, value in enumerate(node):
                walk(value, f'{where}/{index}')

    walk(spec, '#')
    return problems


def check_path_params(spec):
    problems = []
    for path, method, op in operations(spec):
        where = f'{method.upper()} {path}'
        template = set(re.findall(r'{([^}/]+)}', path))
        params = {p.get('name') for p in op.get('parameters', []) if p.get('in') == 'path'}
        for name in sorted(template - params):
            problems.append(f'path-params: {where} has no path parameter {name}')
        for name in sorted(params - template):
            problems.append(f'path-params: {where} declares path parameter {name} that is not in the path')
        for param in op.get('parameters', []):
            if param.get('in') == 'path' and param.get('required') is not True:
                problems.append(f'path-params: {where} path parameter {param.get("name")} is not required')
    return problems


def check_security(spec):
    problems = []
    schemes = spec.get('securityDefinitions', {})
    for name in sorted(set(SECURITY_SCHEMES) - set(schemes)):
        problems.append(f'security: scheme {name} is not declared')
    for name in sorted(set(schemes) - set(SECURITY_SCHEMES)):
        problems.append(f'security: scheme {name} is declared but middleware.Auth accepts no such credential')
    for name, scheme in schemes.items():
        if scheme.get('type') != 'apiKey' or scheme.get('in') != 'header' or scheme.get('name') != SECURITY_HEADER:
            problems.append(f'security: {name} must be an apiKey scheme in the {SECURITY_HEADER} header '
                            '(the middleware reads no other header)')
        if not described(scheme):
            problems.append(f'security: {name} has no description')
        elif name in SECURITY_SCHEMES and f'{SECURITY_HEADER}: {SECURITY_SCHEMES[name]} <' not in scheme['description']:
            problems.append(f'security: {name} description must show the "{SECURITY_HEADER}: '
                            f'{SECURITY_SCHEMES[name]} <...>" form the middleware reads')
    for path, method, op in operations(spec):
        for requirement in op.get('security', []):
            for name in requirement:
                if name not in schemes:
                    problems.append(f'security: {method.upper()} {path} references undeclared scheme {name}')
    return problems


def check_enums(spec, sets):
    problems = []
    index = {display_name(name): schema for name, schema in spec.get('definitions', {}).items()}
    for definition, prop, set_name, allow_empty in ENUM_EXPECTATIONS:
        where = definition + ('.' + prop if prop else '')
        expected = list(sets[set_name]) + ([''] if allow_empty else [])
        if not sets[set_name]:
            problems.append(f'enums: closed set {set_name} resolved to no values (constants renamed?)')
            continue
        schema = index.get(definition)
        if schema is None:
            problems.append(f'enums: {definition} is missing from the spec (update the expectations if it was renamed)')
            continue
        target = schema if prop is None else (schema.get('properties') or {}).get(prop)
        if target is None:
            problems.append(f'enums: {where} is missing from the spec')
            continue
        found = enum_of(spec, target)
        if found is None:
            problems.append(f'enums: {where} has no enum; expected {expected}')
        elif sorted(map(str, found)) != sorted(map(str, expected)) or len(found) != len(set(map(str, found))):
            problems.append(f'enums: {where} enum is {found}; expected {expected} ({set_name})')
    return problems


def security_names(op):
    return {name for requirement in (op.get('security') or []) for name in requirement}


def security_shape(op):
    """None when the operation's security is exactly one alternative per scheme
    (OR), each a single scheme with no scopes; otherwise what is wrong."""
    requirements = op.get('security')
    if not isinstance(requirements, list):
        return 'no security requirement list'
    names = []
    for requirement in requirements:
        if not isinstance(requirement, dict) or len(requirement) != 1:
            return (f'requirement {requirement!r} is not a single scheme (an empty {{}} allows anonymous calls; '
                    'several names in one requirement mean all of them at once)')
        name, scopes = next(iter(requirement.items()))
        if scopes != []:
            return f'requirement {requirement!r} lists scopes'
        names.append(name)
    if sorted(names) != sorted(SECURITY_SCHEMES):
        return f'alternatives {names}; expected one each of {sorted(SECURITY_SCHEMES)}'
    return None


def check_routes(spec, routes):
    problems = []
    if 'security' in spec:
        problems.append('routes: the spec declares a global security requirement; declare it per operation')
    documented = {(method.upper(), path) for path, method, _ in operations(spec)}
    registered = set()
    outside = set()
    for route in routes:
        key = (route['method'], route['path'])
        (registered if route['base'] else outside).add(key)
        op = spec.get('paths', {}).get(route['path'], {}).get(route['method'].lower()) if route['base'] else None
        if op is None:
            continue
        where = f'{route["method"]} {route["path"]}'
        if route.get('auth'):
            wrong = security_shape(op)
            if wrong:
                problems.append(f'routes: {where} runs behind middleware.Auth but its operation security is wrong: {wrong}')
        elif op.get('security'):
            problems.append(f'routes: {where} is public but its operation declares security {op["security"]}')
        answered = {str(code) for code in op.get('responses', {})}
        for code in route.get('responses', []):
            if str(code) not in answered:
                problems.append(f'routes: {where} can be answered {code} by its route middleware but does not document it')
    for method, path in sorted(registered - documented):
        problems.append(f'routes: {method} {path} is routed but has no operation')
    for method, path in sorted(documented - registered):
        problems.append(f'routes: {method} {path} is documented but not routed')
    for method, path in sorted(outside - EXCLUDED_ROUTES):
        problems.append(f'routes: {method} {path} is registered outside the base path and is not an exclusion')
    for method, path in sorted(EXCLUDED_ROUTES - outside):
        problems.append(f'routes: exclusion {method} {path} no longer matches a route; remove it')
    return problems


def check_inputs(spec, inputs):
    problems = []
    for found in inputs:
        where = f'{found["method"]} {found["path"]}'
        op = spec.get('paths', {}).get(found['path'], {}).get(found['method'].lower())
        if op is None:
            problems.append(f'inputs: {where} is annotated on {found["file"]}:{found["line"]} but missing from the spec')
            continue
        params = op.get('parameters', [])
        for location, read in (('query', found['query']), ('formData', found['form'])):
            documented = {p.get('name') for p in params if p.get('in') == location}
            for name in sorted(set(read) - documented):
                problems.append(f'inputs: {where} reads {location} parameter {name} but does not document it')
            for name in sorted(documented - set(read)):
                problems.append(f'inputs: {where} documents {location} parameter {name} that the handler never reads')
        bodies = [p for p in params if p.get('in') == 'body']
        has_form = any(p.get('in') == 'formData' for p in params)
        if bodies and not found['body']:
            problems.append(f'inputs: {where} documents a body parameter but the handler binds no body')
        if not found['body'] or has_form:
            continue
        if len(bodies) != 1:
            problems.append(f'inputs: {where} binds a request body but documents {len(bodies)} body parameters')
            continue
        body = bodies[0]
        types = found.get('body_types') or []
        ref = (body.get('schema') or {}).get('$ref', '')
        documented = display_name(ref[len('#/definitions/'):]) if ref.startswith('#/definitions/') else None
        if len(types) != 1 or documented != types[0]:
            problems.append(f'inputs: {where} body schema is {documented or body.get("schema")}; the handler binds '
                            f'{", ".join(types) or "an unknown type"}')
        if bool(body.get('required')) != bool(found.get('body_required')):
            state = 'refuses a missing body' if found.get('body_required') else 'accepts a missing body'
            problems.append(f'inputs: {where} body parameter required={bool(body.get("required"))} but the handler {state}')
        if '400' not in {str(code) for code in op.get('responses', {})}:
            problems.append(f'inputs: {where} binds a request body (a bind error answers 400) but documents no 400')
    return problems


def check_base_path(spec, expected):
    if spec.get('basePath') != expected:
        return [f'base-path: basePath is {spec.get("basePath")!r}; the router mounts the API at {expected!r} '
                f'(API_PREFIX default in {BASE_PATH_SOURCE.as_posix()})']
    return []


def check_spec(spec, routes, sets, inputs=(), base_path='/api/v1'):
    return (check_base_path(spec, base_path) + check_routes(spec, routes) + check_inputs(spec, inputs)
            + check_tags(spec) + check_operations(spec) + check_properties(spec) + check_refs(spec)
            + check_path_params(spec) + check_security(spec) + check_enums(spec, sets))


def check_artifacts(api_dir, fresh_dir=None):
    problems = []
    api_dir = Path(api_dir)
    present = sorted(p.name for p in api_dir.iterdir() if not p.name.startswith('.')) if api_dir.is_dir() else []
    for name in sorted(set(present) - set(ARTIFACTS)):
        problems.append(f'artifacts: unexpected file api/{name}')
    for name in ARTIFACTS:
        committed = api_dir / name
        if not committed.is_file():
            problems.append(f'artifacts: api/{name} is missing; run make swagger')
            continue
        if committed.stat().st_size > MAX_ARTIFACT_BYTES:
            problems.append(f'size: api/{name} is {committed.stat().st_size} bytes, over {MAX_ARTIFACT_BYTES}')
        if fresh_dir is not None:
            fresh = Path(fresh_dir) / name
            if not fresh.is_file():
                problems.append(f'regenerate: make api-docs did not produce {name}')
            elif fresh.read_bytes() != committed.read_bytes():
                problems.append(f'regenerate: api/{name} differs from a fresh make swagger; regenerate and commit it')
    return problems


def generate(root, out_dir):
    subprocess.run(['make', '--no-print-directory', 'api-docs', f'API_DOCS_DIR={out_dir}'], cwd=root, check=True,
                   stdout=subprocess.DEVNULL)


def extract_source(root):
    """(routes, handler inputs) read from the Go source by scripts/ci/apiroutes."""
    result = subprocess.run(['go', 'run', './scripts/ci/apiroutes', '-root', '.'], cwd=root,
                            capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('route extraction failed:\n' + result.stderr.strip())
    data = json.loads(result.stdout)
    return data['routes'], data['operations']


def configured_base_path(root):
    """The API_PREFIX default the router mounts the API under."""
    match = BASE_PATH_PATTERN.search((Path(root) / BASE_PATH_SOURCE).read_text(encoding='utf-8'))
    if not match:
        raise ValueError(f'no getEnv("API_PREFIX", ...) default in {BASE_PATH_SOURCE.as_posix()}')
    return match.group(1)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    parser.add_argument('--root', default=str(Path(__file__).resolve().parents[2]))
    parser.add_argument('--no-regenerate', action='store_true', help='skip the byte-for-byte regeneration check')
    args = parser.parse_args(argv)
    root = Path(args.root)
    problems = []
    try:
        routes, inputs = extract_source(root)
    except (RuntimeError, OSError, ValueError) as exc:
        print(f'routes: {exc}', file=sys.stderr)
        return 1
    try:
        base_path = configured_base_path(root)
    except (OSError, ValueError) as exc:
        print(f'base-path: {exc}', file=sys.stderr)
        return 1
    try:
        spec = json.loads((root / 'api' / 'swagger.json').read_text(encoding='utf-8'))
    except (OSError, ValueError) as exc:
        print(f'artifacts: cannot read api/swagger.json: {exc}', file=sys.stderr)
        return 1
    problems += check_spec(spec, routes, closed_sets(go_constants(root)), inputs, base_path)
    if args.no_regenerate:
        problems += check_artifacts(root / 'api')
    else:
        with tempfile.TemporaryDirectory(prefix='api-docs-') as folder:
            try:
                generate(root, folder)
            except (subprocess.CalledProcessError, OSError) as exc:
                print(f'regenerate: make api-docs failed: {exc}', file=sys.stderr)
                return 1
            problems += check_artifacts(root / 'api', folder)
    for problem in problems:
        print(problem, file=sys.stderr)
    ops = sum(1 for _ in operations(spec))
    if problems:
        print(f'API docs gate: {len(problems)} problem(s)', file=sys.stderr)
        return 1
    print(f'API docs gate: OK ({ops} operations, {len(routes)} routes, {len(spec.get("definitions", {}))} definitions, '
          f'{len(spec.get("tags", []))} tags)')
    return 0


if __name__ == '__main__':
    sys.exit(main())
