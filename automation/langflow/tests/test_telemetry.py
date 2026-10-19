import os
from unittest.mock import patch

import test_runner as fixtures


class TelemetryTests(fixtures.EngineTests):
    async def test_reports_include_failed_attempts_and_do_not_invent_cost_or_human_minutes(self):
        self.commands.fail_tests = 1
        await self.runner.run_issue(95, execute=True)
        report = self.runner.report(95)
        validation = [a for a in report['attempts'] if a['stage'] == 'validation']
        self.assertEqual([a['outcome'] for a in validation], ['failed', 'success'])
        self.assertEqual(validation[0]['category'], 'implementation/test')
        self.assertEqual(report['provider_calls']['used'], 3)
        self.assertEqual(report['cost']['status'], 'unavailable')
        self.assertIsNone(report['cost']['total_usd'])
        self.assertEqual(report['operator_minutes']['status'], 'unavailable')
        self.assertIsNone(report['operator_minutes']['minutes'])
        with patch.dict(os.environ, {'TASK_SECRET': 'seeded-secret-value'}):
            self.runner.note(95, minutes=3.5, category='intervention', reason='Inspected seeded-secret-value')
            report = self.runner.report(95)
            self.assertNotIn('seeded-secret-value', str(report))
        self.assertEqual(report['operator_minutes']['minutes'], 3.5)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(TelemetryTests, _name, None)
