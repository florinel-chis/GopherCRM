from lfx.custom.custom_component.component import Component
from lfx.inputs import DropdownInput, IntInput, StrInput
from lfx.schema.dataframe import DataFrame
from lfx.template.field.base import Output


class RemediationQueue(Component):
    display_name = 'GitHub remediation queue'
    description = 'Load explicitly configured issues. Preview is the default; execute changes GitHub.'
    icon = 'ListOrdered'
    inputs = [
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
        DropdownInput(name='mode', display_name='Mode', options=['preview', 'execute'], value='preview'),
        IntInput(name='limit', display_name='Maximum tickets', value=1),
    ]
    outputs = [Output(name='tickets', display_name='Tickets', method='load_queue')]

    async def load_queue(self) -> DataFrame:
        import os
        from gophercrm_runner.config import Config, GateError
        from gophercrm_runner.engine import Runner

        path = self.policy_path or os.environ.get('GOPHERCRM_RUNNER_CONFIG', '')
        if not path or self.mode not in {'preview', 'execute'}:
            raise GateError(f'Set a local configuration path and a valid mode (path set: {bool(path)}, mode: {self.mode!r})')
        runner = Runner(Config.load(path))
        tickets = await runner.queue(self.limit)
        self.status = f'{len(tickets)} ticket(s), {self.mode}, target: {runner.config.completion_target}'
        return DataFrame([dict(ticket, config_path=path, mode=self.mode) for ticket in tickets])
