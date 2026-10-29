import { describe, it, expect } from 'vitest';
import { render, screen } from '@/test/test-utils';
import { AutomationStepDetails } from './AutomationStepDetails';
import { OutcomeChip, RunStatusChip } from './AutomationChips';

describe('AutomationStepDetails', () => {
  it('renders known details as labelled pairs with record links', () => {
    render(<AutomationStepDetails details={{ task_id: 7, recipient_id: 3, extra: 'x' }} />);

    expect(screen.getByRole('link', { name: 'Task #7' })).toHaveAttribute('href', '/tasks/7');
    expect(screen.getByText('Recipient:')).toBeInTheDocument();
    expect(screen.getByText('User #3')).toBeInTheDocument();
    expect(screen.getByText('extra:')).toBeInTheDocument();
  });

  it('renders a dash when a step recorded nothing', () => {
    render(<AutomationStepDetails details={{}} />);

    expect(screen.getByText('—')).toBeInTheDocument();
  });
});

describe('AutomationChips', () => {
  it('labels run statuses and outcomes', () => {
    render(
      <>
        <RunStatusChip status="waiting" />
        <OutcomeChip outcome="skipped" />
      </>,
    );

    expect(screen.getByTestId('run-status-chip')).toHaveTextContent('Waiting');
    expect(screen.getByTestId('outcome-chip')).toHaveTextContent('Skipped');
  });
});
