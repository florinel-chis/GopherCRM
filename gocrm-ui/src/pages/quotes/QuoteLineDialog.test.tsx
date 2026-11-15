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
    render(<QuoteLineDialog open quote={quote} onClose={vi.fn()} onSaved={onSaved} />);
    const dialog = await screen.findByRole('dialog', { name: 'Add line' });
    await pickAutocompleteOption('Product', 'Widget (SKU-001)');
    return { dialog, onSaved };
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
});
