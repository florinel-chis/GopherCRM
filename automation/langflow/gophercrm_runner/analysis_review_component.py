from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput, StrInput
from lfx.schema.data import Data
from lfx.template.field.base import Output


class AnalysisReview(Component):
    display_name = 'Review: draft → findings'
    description = "A second, independent read-only agent challenges the draft document and assesses every criterion."
    icon = 'Scale'
    inputs = [
        HandleInput(name='from_specification', display_name='Specification', input_types=['Data', 'JSON'], required=True),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='review', display_name='Review', method='stage_data')]

    async def stage_data(self) -> Data:
        from gophercrm_runner.analysis_support import as_data, config_path, load, upstream_run_id

        output = await load(config_path(self.policy_path)).review(upstream_run_id(self.from_specification))
        self.status = output.markdown
        return as_data(output)
