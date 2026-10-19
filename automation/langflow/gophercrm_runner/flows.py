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
            'targetHandle': {'id': 'Loop-main', 'fieldName': 'item',
                             'inputTypes': ['Message'], 'type': 'other'},
        },
    })
    positions = [(0, 100), (430, 100), (900, 0), (900, 400)]
    for node, (x, y) in zip(payload['data']['nodes'], positions):
        node['position'] = {'x': x, 'y': y}
    for index, edge in enumerate(payload['data']['edges']):
        edge['id'] = f'remediation-edge-{index}'
        edge['sourceHandle'] = json.dumps(edge['data']['sourceHandle'], sort_keys=True)
        edge['targetHandle'] = json.dumps(edge['data']['targetHandle'], sort_keys=True)
    return payload


def export_flow(path: Path):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(build_flow(), indent=2) + '\n')


async def run_flow(config_path, *, execute=False, limit=1):
    from lfx.graph import Graph

    payload = build_flow(str(config_path), mode='execute' if execute else 'preview', limit=limit)
    graph = Graph.from_payload(payload)
    return await graph.arun(inputs=[{}], outputs=['RemediationSummary-main'])
