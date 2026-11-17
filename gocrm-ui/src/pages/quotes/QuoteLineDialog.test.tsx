import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { QuoteLineDialog } from './QuoteLineDialog';
import { productsApi, quotesApi } from '@/api/endpoints';
import { createMockProduct, createMockQuote, createMockQuoteLine } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import type { QuoteLine } from '@/types';

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    addLine: vi.fn(),
    updateLine: vi.fn(),
  },
  productsApi: {
    getProducts: vi.fn(),
    getProduct: vi.fn(),
  },
}));

const TAX_ERROR = 'Tax must be a percentage between 0 and 100 with at most two decimals';

const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const QUANTITY_HINT = 'From 0.001 to 1,000,000, up to three decimals';
const QUANTITY_ERROR = 'Quantity must be a number from 0.001 to 1,000,000 with at most three decimals';

const apiErrorWithDetails = (status: number, code: string, message: string, details: Record<string, unknown>) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details },
  } as AxiosResponse);

const quantityInput = (dialog: HTMLElement): HTMLInputElement =>
  within(dialog).getByLabelText(/^Quantity/) as HTMLInputElement;

/** The Quantity field's helper text, found through the input's aria-describedby. */
const quantityHelperText = (dialog: HTMLElement): string | null => {
  const describedBy = quantityInput(dialog).getAttribute('aria-describedby');
  if (!describedBy) {
    return null;
  }
  return dialog.ownerDocument.getElementById(describedBy)?.textContent ?? null;
};

const taxInput = (dialog: HTMLElement): HTMLInputElement => {
  const input = within(dialog).getByLabelText(/^Tax/) as HTMLInputElement;
  expect(input).toHaveAttribute('name', 'tax_rate');
  return input;
};

describe('QuoteLineDialog', () => {
  const quote = createMockQuote({ id: 42, status: 'draft', currency: 'EUR' });
  const storedLine = (overrides?: Partial<QuoteLine>) =>
    createMockQuoteLine({
      id: 7,
      position: 1,
      description: 'Widget',
      quantity: 2,
      unit_price_cents: 1250,
      discount_bp: 1000,
      line_total_cents: 2250,
      tax_rate_bp: 2100,
      tax_cents: 473,
      ...overrides,
    });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(productsApi.getProducts).mockResolvedValue({
      products: [createMockProduct({ id: 1, name: 'Widget', sku: 'SKU-001', unit_price_cents: 1250, currency: 'EUR' })],
      total: 1,
    });
    vi.mocked(quotesApi.addLine).mockResolvedValue(quote);
    vi.mocked(quotesApi.updateLine).mockResolvedValue(quote);
  });

  const openAdd = async () => {
    const onSaved = vi.fn();
    const view = render(<QuoteLineDialog open quote={quote} onClose={vi.fn()} onSaved={onSaved} />);
    const dialog = await screen.findByRole('dialog', { name: 'Add line' });
    await pickAutocompleteOption('Product', 'Widget (SKU-001)');
    return { dialog, onSaved, view };
  };

  const openEdit = async (line: QuoteLine) => {
    const onSaved = vi.fn();
    const view = render(<QuoteLineDialog open quote={quote} line={line} onClose={vi.fn()} onSaved={onSaved} />);
    const dialog = await screen.findByRole('dialog', { name: 'Edit line' });
    return { dialog, onSaved, view };
  };

  describe('tax rate', () => {
    it('sends tax_rate_bp 2100 for 21 percent when adding a line', async () => {
      const { dialog, onSaved } = await openAdd();

      fireEvent.change(within(dialog).getByLabelText(/^Quantity/), { target: { value: '2' } });
      fireEvent.change(within(dialog).getByLabelText(/^Discount/), { target: { value: '10' } });
      fireEvent.change(taxInput(dialog), { target: { value: '21' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

      await waitFor(() =>
        expect(quotesApi.addLine).toHaveBeenCalledWith(42, {
          product_id: 1,
          quantity: 2,
          discount_bp: 1000,
          tax_rate_bp: 2100,
        })
      );
      await waitFor(() => expect(onSaved).toHaveBeenCalledWith(quote));
    });

    it('starts the add form at 0 percent and sends tax_rate_bp 0 when left unchanged', async () => {
      const { dialog } = await openAdd();

      expect(taxInput(dialog)).toHaveValue('0');
      fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

      await waitFor(() =>
        expect(quotesApi.addLine).toHaveBeenCalledWith(42, {
          product_id: 1,
          quantity: 1,
          discount_bp: 0,
          tax_rate_bp: 0,
        })
      );
    });

    it('starts the edit form from the stored rate and sends the changed rate in basis points', async () => {
      // 12.5 % unchanged: the update states the stored rate.
      const first = await openEdit(storedLine({ tax_rate_bp: 1250 }));
      expect(taxInput(first.dialog)).toHaveValue('12.5');
      fireEvent.click(within(first.dialog).getByRole('button', { name: 'Save' }));
      await waitFor(() =>
        expect(quotesApi.updateLine).toHaveBeenCalledWith(42, 7, {
          description: 'Widget',
          quantity: 2,
          discount_bp: 1000,
          tax_rate_bp: 1250,
          unit_price_cents: 1250,
        })
      );
      first.view.unmount();
      vi.mocked(quotesApi.updateLine).mockClear();

      // 21 % changed to 7.5 %.
      const second = await openEdit(storedLine());
      expect(taxInput(second.dialog)).toHaveValue('21');
      fireEvent.change(taxInput(second.dialog), { target: { value: '7.5' } });
      fireEvent.click(within(second.dialog).getByRole('button', { name: 'Save' }));
      await waitFor(() =>
        expect(quotesApi.updateLine).toHaveBeenCalledWith(42, 7, {
          description: 'Widget',
          quantity: 2,
          discount_bp: 1000,
          tax_rate_bp: 750,
          unit_price_cents: 1250,
        })
      );
      await waitFor(() => expect(second.onSaved).toHaveBeenCalledWith(quote));
    });

    it('sends tax_rate_bp 0 when the stored rate is cleared to 0', async () => {
      const { dialog } = await openEdit(storedLine());

      fireEvent.change(taxInput(dialog), { target: { value: '0' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

      await waitFor(() =>
        expect(quotesApi.updateLine).toHaveBeenCalledWith(42, 7, {
          description: 'Widget',
          quantity: 2,
          discount_bp: 1000,
          tax_rate_bp: 0,
          unit_price_cents: 1250,
        })
      );
    });

    it('refuses a tax rate outside 0 to 100 or with more than two decimals without calling the API', async () => {
      const { dialog, onSaved, view } = await openEdit(storedLine());
      for (const value of ['100.01', '21.555', '-1', 'abc', '']) {
        fireEvent.change(taxInput(dialog), { target: { value } });
        fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
        expect(await within(dialog).findByText(TAX_ERROR)).toBeInTheDocument();
        expect(taxInput(dialog)).toHaveAttribute('aria-invalid', 'true');
      }
      expect(quotesApi.updateLine).not.toHaveBeenCalled();
      expect(onSaved).not.toHaveBeenCalled();
      view.unmount();

      const add = await openAdd();
      fireEvent.change(taxInput(add.dialog), { target: { value: '100.01' } });
      fireEvent.click(within(add.dialog).getByRole('button', { name: 'Add' }));
      expect(await within(add.dialog).findByText(TAX_ERROR)).toBeInTheDocument();
      expect(quotesApi.addLine).not.toHaveBeenCalled();
      expect(add.onSaved).not.toHaveBeenCalled();
    });

    it('shows the server message when the API refuses the line', async () => {
      vi.mocked(quotesApi.addLine).mockRejectedValueOnce(apiError(409, 'CONFLICT', 'quote is not a draft'));
      const add = await openAdd();
      fireEvent.change(taxInput(add.dialog), { target: { value: '21' } });
      fireEvent.click(within(add.dialog).getByRole('button', { name: 'Add' }));

      expect(await within(add.dialog).findByTestId('quote-line-error')).toHaveTextContent('quote is not a draft');
      expect(quotesApi.addLine).toHaveBeenCalledWith(42, expect.objectContaining({ tax_rate_bp: 2100 }));
      expect(screen.getByRole('dialog', { name: 'Add line' })).toBeInTheDocument();
      expect(add.onSaved).not.toHaveBeenCalled();
    });
  });

  describe('quantity', () => {
    const submitButton = (dialog: HTMLElement) =>
      within(dialog).getByRole('button', { name: /^(Add|Save)$/ });

    it('sends quantity 1.5 when adding a line', async () => {
      const { dialog, onSaved } = await openAdd();

      fireEvent.change(quantityInput(dialog), { target: { value: '1.5' } });
      fireEvent.click(submitButton(dialog));

      await waitFor(() => expect(quotesApi.addLine).toHaveBeenCalledTimes(1));
      expect(quotesApi.addLine).toHaveBeenCalledWith(42, {
        product_id: 1,
        quantity: 1.5,
        discount_bp: 0,
        tax_rate_bp: 0,
      });
      await waitFor(() => expect(onSaved).toHaveBeenCalledWith(quote));
      expect(quotesApi.addLine).toHaveBeenCalledTimes(1);
    });

    it('accepts the bounds 0.001 and 1000000 and sends them as numbers', async () => {
      const cases: Array<[string, number]> = [
        ['0.001', 0.001],
        ['1000000', 1000000],
        ['1000000.000', 1000000],
        ['999999.999', 999999.999],
      ];
      for (const [text, sent] of cases) {
        vi.mocked(quotesApi.addLine).mockClear();
        const { dialog, onSaved, view } = await openAdd();
        fireEvent.change(quantityInput(dialog), { target: { value: text } });
        fireEvent.click(submitButton(dialog));

        await waitFor(() => expect(onSaved).toHaveBeenCalledWith(quote));
        expect(quotesApi.addLine).toHaveBeenCalledTimes(1);
        expect(quotesApi.addLine).toHaveBeenCalledWith(42, {
          product_id: 1,
          quantity: sent,
          discount_bp: 0,
          tax_rate_bp: 0,
        });
        expect(quantityInput(dialog)).not.toHaveAttribute('aria-invalid', 'true');
        view.unmount();
      }
    });

    it('refuses a quantity outside 0.001 to 1,000,000 or with more than three decimals without calling the API', async () => {
      const refused = [
        '0',
        '0.000',
        '0.0001',
        '1.2345',
        '1.5000',
        '1000000.001',
        '1000001',
        '-1',
        '+1',
        '.5',
        '1.',
        '1e3',
        '1,5',
        'abc',
        '',
      ];
      const assertRefused = (dialog: HTMLElement, value: string) => {
        fireEvent.change(quantityInput(dialog), { target: { value } });
        expect(quantityHelperText(dialog)).toBe(QUANTITY_HINT);
        fireEvent.click(submitButton(dialog));
        const input = quantityInput(dialog);
        expect(input, value).toHaveAttribute('aria-invalid', 'true');
        expect(quantityHelperText(dialog), value).toBe(QUANTITY_ERROR);
      };

      const add = await openAdd();
      for (const value of refused) {
        assertRefused(add.dialog, value);
      }
      expect(quotesApi.addLine).not.toHaveBeenCalled();
      expect(add.onSaved).not.toHaveBeenCalled();
      add.view.unmount();

      const edit = await openEdit(storedLine());
      for (const value of refused) {
        assertRefused(edit.dialog, value);
      }
      expect(quotesApi.updateLine).not.toHaveBeenCalled();
      expect(edit.onSaved).not.toHaveBeenCalled();
    });

    it('prefills the edit form with the stored fractional quantity and sends the changed value', async () => {
      const { dialog, onSaved } = await openEdit(storedLine({ quantity: 2.5 }));

      expect(quantityInput(dialog)).toHaveValue('2.5');
      fireEvent.change(quantityInput(dialog), { target: { value: '0.125' } });
      fireEvent.click(submitButton(dialog));

      await waitFor(() => expect(quotesApi.updateLine).toHaveBeenCalledTimes(1));
      expect(quotesApi.updateLine).toHaveBeenCalledWith(42, 7, {
        description: 'Widget',
        quantity: 0.125,
        discount_bp: 1000,
        tax_rate_bp: 2100,
        unit_price_cents: 1250,
      });
      await waitFor(() => expect(onSaved).toHaveBeenCalledWith(quote));
    });

    it('shows the API quantity message on the Quantity field instead of the alert', async () => {
      const message = 'quantity must be a number from 0.001 to 1000000 with at most 3 decimal places';
      const refusal = () =>
        apiErrorWithDetails(400, 'VALIDATION_ERROR', 'Validation failed', { quantity: message });

      // Add: POST /quotes/{id}/lines.
      vi.mocked(quotesApi.addLine).mockRejectedValueOnce(refusal());
      const add = await openAdd();
      fireEvent.change(quantityInput(add.dialog), { target: { value: '2.5' } });
      fireEvent.click(submitButton(add.dialog));

      await waitFor(() => expect(quantityHelperText(add.dialog)).toBe(message));
      expect(quotesApi.addLine).toHaveBeenCalledWith(42, expect.objectContaining({ quantity: 2.5 }));
      expect(quantityInput(add.dialog)).toHaveAttribute('aria-invalid', 'true');
      expect(within(add.dialog).queryByTestId('quote-line-error')).not.toBeInTheDocument();
      expect(within(add.dialog).queryByText('Validation failed')).not.toBeInTheDocument();
      expect(screen.getByRole('dialog', { name: 'Add line' })).toBeInTheDocument();
      expect(add.onSaved).not.toHaveBeenCalled();

      fireEvent.change(quantityInput(add.dialog), { target: { value: '2' } });
      expect(quantityHelperText(add.dialog)).toBe(QUANTITY_HINT);
      expect(quantityInput(add.dialog)).not.toHaveAttribute('aria-invalid', 'true');
      add.view.unmount();

      // Edit: PUT /quotes/{id}/lines/{lineId}.
      vi.mocked(quotesApi.updateLine).mockRejectedValueOnce(refusal());
      const edit = await openEdit(storedLine());
      fireEvent.change(quantityInput(edit.dialog), { target: { value: '0.5' } });
      fireEvent.click(submitButton(edit.dialog));

      await waitFor(() => expect(quantityHelperText(edit.dialog)).toBe(message));
      expect(quotesApi.updateLine).toHaveBeenCalledWith(42, 7, expect.objectContaining({ quantity: 0.5 }));
      expect(quantityInput(edit.dialog)).toHaveAttribute('aria-invalid', 'true');
      expect(within(edit.dialog).queryByTestId('quote-line-error')).not.toBeInTheDocument();
      expect(screen.getByRole('dialog', { name: 'Edit line' })).toBeInTheDocument();
      expect(edit.onSaved).not.toHaveBeenCalled();

      fireEvent.change(quantityInput(edit.dialog), { target: { value: '3' } });
      expect(quantityHelperText(edit.dialog)).toBe(QUANTITY_HINT);
      expect(quantityInput(edit.dialog)).not.toHaveAttribute('aria-invalid', 'true');
    });

    it('keeps a refusal without details.quantity in the alert', async () => {
      // VALIDATION_ERROR keyed on another field (add).
      vi.mocked(quotesApi.addLine).mockRejectedValueOnce(
        apiErrorWithDetails(400, 'VALIDATION_ERROR', 'Validation failed', { DiscountBP: 'must be at most 10000' })
      );
      const add = await openAdd();
      fireEvent.click(submitButton(add.dialog));
      expect(await within(add.dialog).findByTestId('quote-line-error')).toHaveTextContent('Validation failed');
      expect(quantityInput(add.dialog)).not.toHaveAttribute('aria-invalid', 'true');
      expect(quantityHelperText(add.dialog)).toBe(QUANTITY_HINT);
      add.view.unmount();

      // A non-string details.quantity (edit).
      vi.mocked(quotesApi.updateLine).mockRejectedValueOnce(
        apiErrorWithDetails(400, 'VALIDATION_ERROR', 'Validation failed', { quantity: 5 })
      );
      const edit = await openEdit(storedLine());
      fireEvent.click(submitButton(edit.dialog));
      expect(await within(edit.dialog).findByTestId('quote-line-error')).toHaveTextContent('Validation failed');
      expect(quantityInput(edit.dialog)).not.toHaveAttribute('aria-invalid', 'true');
      edit.view.unmount();

      // Another code, even with a quantity key (edit).
      vi.mocked(quotesApi.updateLine).mockRejectedValueOnce(
        apiErrorWithDetails(409, 'CONFLICT', 'quote is not a draft', { quantity: 'ignored' })
      );
      const conflict = await openEdit(storedLine());
      fireEvent.click(submitButton(conflict.dialog));
      expect(await within(conflict.dialog).findByTestId('quote-line-error')).toHaveTextContent('quote is not a draft');
      expect(quantityInput(conflict.dialog)).not.toHaveAttribute('aria-invalid', 'true');
      expect(quantityHelperText(conflict.dialog)).toBe(QUANTITY_HINT);
    });

    it('uses a text input with decimal input mode named quantity and shows the range hint', async () => {
      const { dialog, view } = await openAdd();
      const input = quantityInput(dialog);
      expect(input).toHaveAttribute('type', 'text');
      expect(input).toHaveAttribute('inputmode', 'decimal');
      expect(input).toHaveAttribute('name', 'quantity');
      expect(input).not.toHaveAttribute('min');
      expect(input).not.toHaveAttribute('max');
      expect(input).not.toHaveAttribute('step');
      expect(input).toHaveValue('1');
      expect(input).not.toHaveAttribute('aria-invalid', 'true');
      expect(quantityHelperText(dialog)).toBe(QUANTITY_HINT);
      view.unmount();

      const edit = await openEdit(storedLine());
      const editInput = quantityInput(edit.dialog);
      expect(editInput).toHaveAttribute('type', 'text');
      expect(editInput).toHaveAttribute('inputmode', 'decimal');
      expect(editInput).toHaveAttribute('name', 'quantity');
      expect(editInput).not.toHaveAttribute('step');
      expect(quantityHelperText(edit.dialog)).toBe(QUANTITY_HINT);
    });
  });
});
