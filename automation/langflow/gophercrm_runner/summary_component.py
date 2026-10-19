from lfx.custom.custom_component.component import Component
from lfx.inputs import HandleInput
from lfx.schema.message import Message
from lfx.template.field.base import Output


class RemediationSummary(Component):
    display_name = 'Remediation results'
    description = 'Results from the completed queue. The workflow stops at its first failed ticket.'
    icon = 'ListChecks'
    inputs = [HandleInput(name='results', display_name='Results', input_types=['DataFrame', 'Table'])]
    outputs = [Output(name='summary', display_name='Summary', method='summarize')]

    async def summarize(self) -> Message:
        import json

        rows = [row.data for row in self.results.to_data_list()]
        self.status = f'{len(rows)} ticket(s) completed'
        return Message(text=json.dumps(rows, default=str))
