from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput, StrInput
from lfx.schema.data import Data
from lfx.template.field.base import Output


class AnalysisResearch(Component):
    display_name = 'Research: brief → evidence'
    description = "Establishes what exists today and what the links and the wider context say, with citations. Read-only; web tools allowed."
    icon = 'Search'
    inputs = [
        HandleInput(name='from_intake', display_name='Brief', input_types=['Data', 'JSON'], required=True),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='research', display_name='Research', method='stage_data')]

    async def stage_data(self) -> Data:
        from gophercrm_runner.analysis_support import as_data, config_path, load, upstream_run_id

        output = await load(config_path(self.policy_path)).research(upstream_run_id(self.from_intake))
        self.status = output.markdown
        return as_data(output)
