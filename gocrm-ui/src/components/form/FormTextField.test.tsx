import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { useForm, FormProvider } from 'react-hook-form';
import { FormTextField } from './FormTextField';

const TestWrapper = ({ children }: { children: React.ReactNode }) => {
  const methods = useForm({
    defaultValues: {
      testField: '',
    },
  });

  return <FormProvider {...methods}>{children}</FormProvider>;
};

describe('FormTextField', () => {
  it('renders with label', () => {
    render(
      <TestWrapper>
        <FormTextField name="testField" label="Test Label" />
      </TestWrapper>
    );

    expect(screen.getByLabelText('Test Label')).toBeInTheDocument();
  });

  it('shows required indicator', () => {
    render(
      <TestWrapper>
        <FormTextField name="testField" label="Test Label" required />
      </TestWrapper>
    );

    expect(screen.getByLabelText('Test Label *')).toBeInTheDocument();
  });

  it('handles input changes', () => {
    render(
      <TestWrapper>
        <FormTextField name="testField" label="Test Label" />
      </TestWrapper>
    );

    const input = screen.getByLabelText('Test Label');
    fireEvent.change(input, { target: { value: 'test value' } });

    expect(input).toHaveValue('test value');
  });

  it('displays error message', () => {
    const TestWrapperWithError = ({ children }: { children: React.ReactNode }) => {
      const methods = useForm({
        defaultValues: {
          testField: '',
        },
      });

      // Manually set error
      methods.setError('testField', { message: 'Field is required' });

      return <FormProvider {...methods}>{children}</FormProvider>;
    };

    render(
      <TestWrapperWithError>
        <FormTextField name="testField" label="Test Label" />
      </TestWrapperWithError>
    );

    expect(screen.getByText('Field is required')).toBeInTheDocument();
  });

  it('accepts TextField props', () => {
    render(
      <TestWrapper>
        <FormTextField
          name="testField"
          label="Test Label"
          placeholder="Enter text"
          disabled
          multiline
          rows={4}
        />
      </TestWrapper>
    );

    const input = screen.getByLabelText('Test Label');
    expect(input).toHaveAttribute('placeholder', 'Enter text');
    expect(input).toBeDisabled();
    expect(input).toHaveAttribute('rows', '4');
  });

  it('defaults to fullWidth', () => {
    const { container } = render(
      <TestWrapper>
        <FormTextField name="testField" label="Test Label" />
      </TestWrapper>
    );

    const textField = container.querySelector('.MuiFormControl-fullWidth');
    expect(textField).toBeInTheDocument();
  });
});