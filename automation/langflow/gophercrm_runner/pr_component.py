from lfx.custom.custom_component.component import Component
from lfx.inputs import BoolInput, DropdownInput, IntInput, StrInput
from lfx.schema.message import Message
from lfx.template.field.base import Output


class CompletePullRequest(Component):
    display_name = 'Review → test → merge existing PR'
    description = 'Pinned PR, tests and documentation review, bounded repairs, exact-head merge and documentation verification.'
    icon = 'GitMerge'
    inputs = [
        StrInput(name='policy_path', display_name='Local configuration path', value='',
                 info='Blank uses GOPHERCRM_RUNNER_CONFIG. The issue queue remains unchanged.'),
        IntInput(name='pr_number', display_name='PR number', value=0),
        StrInput(name='expected_head', display_name='Expected head (full SHA)', value=''),
        DropdownInput(name='mode', display_name='Mode', options=['preview', 'execute'], value='preview'),
        BoolInput(name='refresh', display_name='Refresh changed candidate', value=False,
                  info='Invalidate previous checks after an inspected head/base change; retain spent budgets.'),
    ]
    outputs = [Output(name='result', display_name='Result', method='complete')]

    async def complete(self) -> Message:
        import json
        import os
        from gophercrm_runner.config import Config, GateError
        from gophercrm_runner.pull_requests import PullRequestRunner

        path = self.policy_path or os.environ.get('GOPHERCRM_RUNNER_CONFIG', '')
        if not path or self.mode not in {'preview', 'execute'}:
            raise GateError('Supply a local policy and a valid mode')
        runner = PullRequestRunner(Config.load(path))
        result = await runner.complete_pr(self.pr_number, self.expected_head,
                                          execute=self.mode == 'execute', refresh=self.refresh)
        self.status = result['status']
        return Message(text=json.dumps(result))
