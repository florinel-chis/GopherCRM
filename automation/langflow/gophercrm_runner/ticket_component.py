from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput
from lfx.schema.data import Data
from lfx.schema.message import Message
from lfx.template.field.base import Output


class RemediateTicket(Component):
    display_name = 'Implement → test → review → merge'
    description = 'One isolated worktree. Bounded repair loop, independent Claude review, all CI gates.'
    icon = 'GitPullRequest'
    inputs = [HandleInput(name='ticket', display_name='Ticket', input_types=['Data', 'JSON'], required=True)]
    outputs = [Output(name='result', display_name='Result', method='run_ticket')]

    async def run_ticket(self) -> Message:
        import json
        from gophercrm_runner.config import Config, GateError
        from gophercrm_runner.engine import Runner

        item = self.ticket.data if isinstance(self.ticket, Data) else None
        if not isinstance(item, dict) or item.get('mode') not in {'preview', 'execute'}:
            raise GateError('Invalid queue item')
        runner = Runner(Config.load(item['config_path']))
        self.status = f'Issue #{item["issue"]}: {item["mode"]}'
        result = await runner.run_issue(item['issue'], execute=item['mode'] == 'execute')
        self.status = result['status']
        return Message(text=json.dumps(result))
