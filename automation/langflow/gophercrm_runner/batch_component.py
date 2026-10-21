from lfx.custom.custom_component.component import Component
from lfx.inputs import BoolInput, DropdownInput, StrInput
from lfx.schema.message import Message
from lfx.template.field.base import Output


class RunPreparedBatch(Component):
    display_name = 'Prepared batch → review → test → merge'
    description = 'Sequential prepared tickets with durable budgets, recovery and stop-after-current.'
    icon = 'ListChecks'
    inputs = [
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG.'),
        StrInput(name='batch_id', display_name='Prepared batch ID', value=''),
        DropdownInput(name='mode', display_name='Mode', options=['preview', 'status', 'execute'], value='preview'),
        BoolInput(name='resume', display_name='Resume a stopped batch', value=False,
                  info='Explicitly continue after inspection; preserves spent budgets and frozen inputs.'),
        StrInput(name='control_snapshot', display_name='Launch control snapshot', value='', advanced=True,
                 info='Supplied by the launcher so stop requests during startup take precedence.'),
    ]
    outputs = [Output(name='result', display_name='Progress and result', method='run_batch')]

    async def run_batch(self) -> Message:
        import json
        import os
        from gophercrm_runner.batches import BatchRunner
        from gophercrm_runner.config import Config, GateError

        path = self.policy_path or os.environ.get('GOPHERCRM_RUNNER_CONFIG', '')
        if not path or self.mode not in {'preview', 'status', 'execute'}:
            raise GateError('Supply a local policy and a valid batch mode')
        runner = BatchRunner(Config.load(path), config_path=path)
        if self.mode == 'status':
            result = runner.status(self.batch_id)
        else:
            result = await runner.run(self.batch_id, execute=self.mode == 'execute', resume=self.resume,
                                      control_snapshot=self.control_snapshot)
        self.status = result
        return Message(text=json.dumps(result))
