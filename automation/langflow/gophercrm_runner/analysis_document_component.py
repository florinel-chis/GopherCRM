from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput, StrInput
from lfx.schema.message import Message
from lfx.template.field.base import Output


class AnalysisDocument(Component):
    display_name = 'Document: write docs/specs/<date>-<slug>.md'
    description = 'Renders the one-pager, the appendices and the manifest, writes the gitignored document and a private copy, and removes the worktree.'
    icon = 'FileCheck'
    inputs = [
        HandleInput(name='from_revision', display_name='Revision', input_types=['Data', 'JSON'], required=True),
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG from the Langflow server environment.'),
    ]
    outputs = [Output(name='document', display_name='Document', method='publish')]

    async def publish(self) -> Message:
        from gophercrm_runner.analysis_support import config_path, load, upstream_run_id

        output = await load(config_path(self.policy_path)).document(upstream_run_id(self.from_revision))
        self.status = output.markdown
        return Message(text=output.markdown)
