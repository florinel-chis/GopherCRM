from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput, StrInput
from lfx.schema.data import Data
from lfx.template.field.base import Output


class AnalysisRevision(Component):
    display_name = 'Revision: findings → revised draft'
    description = "One bounded revision addressing every finding by id; declined findings stay visible."
    icon = 'Pencil'
    inputs = [
        HandleInput(name='from_review', display_name='Review', input_types=['Data', 'JSON'], required=True),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='revision', display_name='Revision', method='stage_data')]

    async def stage_data(self) -> Data:
        from gophercrm_runner.analysis_support import as_data, config_path, load, upstream_run_id

        output = await load(config_path(self.policy_path)).revision(upstream_run_id(self.from_review))
        self.status = output.markdown
        return as_data(output)
