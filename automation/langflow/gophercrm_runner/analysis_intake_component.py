from lfx.custom.custom_component.component import Component
from lfx.inputs import DropdownInput, MultilineInput, StrInput
from lfx.schema.data import Data
from lfx.template.field.base import Output


class AnalysisIntake(Component):
    display_name = 'Intake: request → brief'
    description = 'Restates a feature request in the repository\'s own terms from a detached worktree at origin/main. Read-only.'
    icon = 'MessageSquareText'
    inputs = [
        MultilineInput(name='wish', display_name='Feature request', value='',
                       info='What should exist, in your own words. Blank in a refinement (the parent request is kept).'),
        MultilineInput(name='links', display_name='Reference links', value='',
                       info='Optional, one absolute http(s) URL per line, at most ten.'),
        MultilineInput(name='comments', display_name='Comments on the previous revision', value='',
                       info='Refinement only: blank lines separate comments; the runner numbers them C-1, C-2, …'),
        StrInput(name='previous_run_id', display_name='Previous run id', value='',
                 info='Refinement only: the run whose document the comments answer.'),
        DropdownInput(name='refine_from', display_name='Refine from stage',
                      options=['intake', 'research', 'analysis', 'specification'], value='research',
                      info='Refinement only: earlier stages are inherited from the previous run.'),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='brief', display_name='Brief', method='stage_data')]

    async def stage_data(self) -> Data:
        from gophercrm_runner.analysis_support import as_data, config_path, load

        output = await load(config_path(self.policy_path)).intake(
            wish=str(self.wish or ''), links=str(self.links or ''), comments=str(self.comments or ''),
            previous_run_id=str(self.previous_run_id or ''), refine_from=str(self.refine_from or 'research'))
        self.status = f'Run id: {output.run.run_id}\n\n' + output.markdown
        return as_data(output)
