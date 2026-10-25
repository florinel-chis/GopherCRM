from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput, StrInput
from lfx.schema.data import Data
from lfx.template.field.base import Output


class AnalysisAnalysis(Component):
    display_name = 'Analysis: evidence → one-pager'
    description = "Problem, users, proposal, scope, main flow, options, risks, size and the decision needed, within 500 words."
    icon = 'FileText'
    inputs = [
        HandleInput(name='from_research', display_name='Research', input_types=['Data', 'JSON'], required=True),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='analysis', display_name='Analysis', method='stage_data')]

    async def stage_data(self) -> Data:
        from gophercrm_runner.analysis_support import as_data, config_path, load, upstream_run_id

        output = await load(config_path(self.policy_path)).analysis(upstream_run_id(self.from_research))
        self.status = output.markdown
        return as_data(output)
