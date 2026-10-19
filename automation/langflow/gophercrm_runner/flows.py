"""Generate importable Langflow JSON using the pinned LFX component schemas."""

from pathlib import Path
import json


def build_flow(config_path='', *, mode='preview', limit=1):
    from lfx.components.flow_controls.loop import LoopComponent
    from lfx.graph import Graph
    from .queue_component import RemediationQueue
    from .ticket_component import RemediateTicket
    from .summary_component import RemediationSummary

    graph = Graph()
    queue = RemediationQueue(policy_path=config_path, mode=mode, limit=limit)
    graph.add_component(queue, 'RemediationQueue-main')
    graph.add_component(LoopComponent(), 'Loop-main')
    graph.add_component(RemediateTicket(), 'RemediateTicket-main')
    graph.add_component(RemediationSummary(), 'RemediationSummary-main')
    graph.add_component_edge('RemediationQueue-main', ('tickets', 'data'), 'Loop-main')
    graph.add_component_edge('Loop-main', ('item', 'ticket'), 'RemediateTicket-main')
    graph.add_component_edge('Loop-main', ('done', 'results'), 'RemediationSummary-main')
    graph.initialize()
    payload = graph.dump(name='GopherCRM remediation runner',
                         description='Explicit GitHub queue → sequential Loop → implement/test/review/merge. Preview by default.')
    # Loop's feedback handle is a synthetic input supplied by the editor, not a
    # regular Component input, so add that edge to the public serialized schema.
    payload['data']['edges'].append({
        'source': 'RemediateTicket-main', 'target': 'Loop-main',
        'data': {
            'sourceHandle': {'id': 'RemediateTicket-main', 'name': 'result',
                             'dataType': 'RemediateTicket', 'output_types': ['Message']},
            'targetHandle': {'id': 'Loop-main', 'name': 'item',
                             'dataType': 'LoopComponent', 'output_types': ['JSON', 'Message']},
        },
    })
    positions = {'RemediationQueue-main': (0, 100), 'Loop-main': (430, 100),
                 'RemediateTicket-main': (900, 0), 'RemediationSummary-main': (900, 400)}
    payload['data']['nodes'].sort(key=lambda node: node['id'])
    for node in payload['data']['nodes']:
        x, y = positions[node['id']]
        node['position'] = {'x': x, 'y': y}
        node['type'] = 'genericNode'
        for output in node['data']['node']['outputs']:
            if 'JSON' in output['types']:
                output['selected'] = 'JSON'
            elif 'Table' in output['types']:
                output['selected'] = 'Table'
        node['data']['selected_output'] = node['data']['node']['outputs'][0]['name']
    payload['data']['edges'].sort(key=lambda edge: (
        edge['source'], edge['target'], edge['data']['sourceHandle']['name'],
        edge['data']['targetHandle'].get('fieldName', edge['data']['targetHandle'].get('name'))))
    nodes = {node['id']: node for node in payload['data']['nodes']}
    for index, edge in enumerate(payload['data']['edges']):
        source = edge['data']['sourceHandle']
        output = next(o for o in nodes[edge['source']]['data']['node']['outputs'] if o['name'] == source['name'])
        source['output_types'] = [output['selected']]
        target = edge['data']['targetHandle']
        if 'fieldName' in target:
            target['type'] = nodes[edge['target']]['data']['node']['template'][target['fieldName']]['type']
        edge['id'] = f'remediation-edge-{index}'
        # The visual editor uses its escaped handle encoding in DOM identifiers.
        edge['sourceHandle'] = json.dumps(source, sort_keys=True, separators=(',', ':')).replace('"', 'œ')
        edge['targetHandle'] = json.dumps(target, sort_keys=True, separators=(',', ':')).replace('"', 'œ')
    return payload


def export_flow(path: Path):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(build_flow(), indent=2) + '\n')


async def run_flow(config_path, *, execute=False, limit=1):
    from lfx.graph import Graph

    payload = build_flow(str(config_path), mode='execute' if execute else 'preview', limit=limit)
    graph = Graph.from_payload(payload)
    return await graph.arun(inputs=[{}], outputs=['RemediationSummary-main'])
