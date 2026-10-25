from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput, StrInput
from lfx.schema.data import Data
from lfx.template.field.base import Output


class AnalysisSpecification(Component):
    display_name = 'Specification: one-pager → detail'
    description = "Acceptance criteria in the runner's manifest shape, data model with both migration halves, API, screens, permissions, diagrams, test plan."
    icon = 'ListChecks'
    inputs = [
        HandleInput(name='from_analysis', display_name='Analysis', input_types=['Data', 'JSON'], required=True),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='specification', display_name='Specification', method='stage_data')]

    async def stage_data(self) -> Data:
        from gophercrm_runner.analysis_support import as_data, config_path, load, upstream_run_id

        output = await load(config_path(self.policy_path)).specification(upstream_run_id(self.from_analysis))
        self.status = output.markdown
        return as_data(output)
