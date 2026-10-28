import React, { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
} from '@mui/material';
import { quotesApi, type QuoteLineData, type UpdateQuoteLineData } from '@/api/endpoints';
import type { Product, Quote, QuoteLine } from '@/types';
import { ProductAutocomplete } from '@/components/ProductAutocomplete';
import { centsToDecimal, decimalToCents } from '@/pages/deals/dealFormat';
import { getAPIError } from '@/utils/apiError';
import { basisPointsToPercent, formatMoney, percentToBasisPoints } from './quoteFormat';

export interface QuoteLineDialogProps {
  open: boolean;
  quote: Quote;
  /** The line to edit; absent for "Add line". */
  line?: QuoteLine | null;
  onClose: () => void;
  /** Called with the quote the API returned, lines and totals recomputed. */
  onSaved: (quote: Quote) => void;
}

const DECIMAL_PATTERN = /^\d+(\.\d{1,2})?$/;
const QUANTITY_MAX = 1_000_000;
const PRICE_CENTS_MAX = 1_000_000_000_000;
const DESCRIPTION_MAX_LENGTH = 255;

interface FieldErrors {
  product?: string;
  description?: string;
  quantity?: string;
  discount?: string;
  unit_price?: string;
}

type QuoteLineFormProps = Omit<QuoteLineDialogProps, 'open' | 'line'> & { line: QuoteLine | null };

// The dialog's content. MUI unmounts it while the dialog is closed, so every
// opening starts from the line (or empty) with no errors left over.
const QuoteLineForm: React.FC<QuoteLineFormProps> = ({ quote, line, onClose, onSaved }) => {
  const queryClient = useQueryClient();
  const isEdit = line !== null;

  const [productId, setProductId] = useState<number | null>(null);
  const [product, setProduct] = useState<Product | null>(null);
  const [description, setDescription] = useState(line?.description ?? '');
  const [quantity, setQuantity] = useState(line ? String(line.quantity) : '1');
  const [discount, setDiscount] = useState(line ? basisPointsToPercent(line.discount_bp) : '0');
  // Add: an optional override of the product's price. Edit: the stored price.
  const [unitPrice, setUnitPrice] = useState(line ? centsToDecimal(line.unit_price_cents) : '');
  const [errors, setErrors] = useState<FieldErrors>({});
  const [serverError, setServerError] = useState<string | null>(null);

  const finish = (updated: Quote) => {
    queryClient.setQueryData(['quote', quote.id], updated);
    queryClient.invalidateQueries({ queryKey: ['quotes'] });
    onSaved(updated);
  };
  const fail = (error: unknown, fallback: string) => {
    setServerError(getAPIError(error)?.message || fallback);
  };

  const addMutation = useMutation({
    mutationFn: (data: QuoteLineData) => quotesApi.addLine(quote.id, data),
    onSuccess: finish,
    onError: (error) => fail(error, 'Failed to add the line'),
  });

  const updateMutation = useMutation({
    mutationFn: (data: UpdateQuoteLineData) => quotesApi.updateLine(quote.id, (line as QuoteLine).id, data),
    onSuccess: finish,
    onError: (error) => fail(error, 'Failed to update the line'),
  });

  const pending = addMutation.isPending || updateMutation.isPending;

  const validate = (): FieldErrors => {
    const next: FieldErrors = {};
    if (!isEdit && productId === null) {
      next.product = 'Pick a product';
    }
    if (isEdit && description.trim() === '') {
      next.description = 'Description is required';
    } else if (description.trim().length > DESCRIPTION_MAX_LENGTH) {
      next.description = `Description must be ${DESCRIPTION_MAX_LENGTH} characters or fewer`;
    }
    const quantityValue = Number(quantity.trim());
    if (!/^\d+$/.test(quantity.trim()) || quantityValue < 1 || quantityValue > QUANTITY_MAX) {
      next.quantity = 'Quantity must be a whole number between 1 and 1,000,000';
    }
    const discountText = discount.trim();
    if (!DECIMAL_PATTERN.test(discountText) || percentToBasisPoints(discountText) > 10000) {
      next.discount = 'Discount must be a percentage between 0 and 100 with at most two decimals';
    }
    const priceText = unitPrice.trim();
    if (priceText !== '' || isEdit) {
      if (!DECIMAL_PATTERN.test(priceText) || decimalToCents(priceText) > PRICE_CENTS_MAX) {
        next.unit_price = 'Unit price must be a non-negative number with at most two decimals';
      }
    }
    return next;
  };

  const submit = () => {
    const next = validate();
    setErrors(next);
    if (Object.keys(next).length > 0) {
      return;
    }
    setServerError(null);
    const quantityValue = Number(quantity.trim());
    const discountBP = percentToBasisPoints(discount.trim());
    if (isEdit) {
      updateMutation.mutate({
        description: description.trim(),
        quantity: quantityValue,
        discount_bp: discountBP,
        unit_price_cents: decimalToCents(unitPrice.trim()),
      });
      return;
    }
    const data: QuoteLineData = {
      product_id: productId as number,
      quantity: quantityValue,
      discount_bp: discountBP,
    };
    if (unitPrice.trim() !== '') {
      data.unit_price_cents = decimalToCents(unitPrice.trim());
    }
    if (description.trim() !== '') {
      data.description = description.trim();
    }
    addMutation.mutate(data);
  };

  const productHelper = errors.product ?? (product
    ? `Unit price ${formatMoney(product.unit_price_cents, product.currency)}`
    : `Active products priced in ${quote.currency}`);

  return (
    <>
      <DialogTitle id="quote-line-dialog-title">{isEdit ? 'Edit line' : 'Add line'}</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ mt: 1 }}>
          {!isEdit && (
            <ProductAutocomplete
              currency={quote.currency}
              value={productId}
              onChange={(nextId, record) => {
                setProductId(nextId);
                setProduct(record);
                setErrors((prev) => ({ ...prev, product: undefined }));
              }}
              error={!!errors.product}
              helperText={productHelper}
            />
          )}
          <TextField
            label="Description"
            value={description}
            onChange={(event) => {
              setDescription(event.target.value);
              setErrors((prev) => ({ ...prev, description: undefined }));
            }}
            error={!!errors.description}
            helperText={errors.description ?? (isEdit ? undefined : 'Optional; defaults to the product name')}
            inputProps={{ maxLength: DESCRIPTION_MAX_LENGTH, name: 'description' }}
            fullWidth
          />
          <TextField
            label="Quantity"
            type="number"
            value={quantity}
            onChange={(event) => {
              setQuantity(event.target.value);
              setErrors((prev) => ({ ...prev, quantity: undefined }));
            }}
            inputProps={{ min: 1, max: QUANTITY_MAX, step: 1, name: 'quantity' }}
            error={!!errors.quantity}
            helperText={errors.quantity}
            fullWidth
          />
          <TextField
            label="Discount (%)"
            value={discount}
            onChange={(event) => {
              setDiscount(event.target.value);
              setErrors((prev) => ({ ...prev, discount: undefined }));
            }}
            inputProps={{ inputMode: 'decimal', name: 'discount' }}
            error={!!errors.discount}
            helperText={errors.discount ?? 'Per line, up to two decimals; sent as basis points'}
            fullWidth
          />
          <TextField
            label={isEdit ? 'Unit price' : 'Unit price override'}
            value={unitPrice}
            onChange={(event) => {
              setUnitPrice(event.target.value);
              setErrors((prev) => ({ ...prev, unit_price: undefined }));
            }}
            placeholder={isEdit ? undefined : 'Product price'}
            inputProps={{ inputMode: 'decimal', name: 'unit_price' }}
            error={!!errors.unit_price}
            helperText={errors.unit_price ?? `In ${quote.currency}${isEdit ? '' : '; leave empty to use the product price'}`}
            fullWidth
          />
          {serverError && (
            <Alert severity="error" data-testid="quote-line-error">
              {serverError}
            </Alert>
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={pending}>
          Cancel
        </Button>
        <Button variant="contained" onClick={submit} disabled={pending}>
          {isEdit ? 'Save' : 'Add'}
        </Button>
      </DialogActions>
    </>
  );
};

/**
 * Adds a line to a draft quote (a picker of the active products in the
 * quote's currency, the quantity, the discount as a percentage, an optional
 * price override) or edits one. The discount is sent in basis points.
 */
export const QuoteLineDialog: React.FC<QuoteLineDialogProps> = ({ open, quote, line = null, onClose, onSaved }) => (
  <Dialog open={open} onClose={onClose} aria-labelledby="quote-line-dialog-title" fullWidth maxWidth="sm">
    <QuoteLineForm quote={quote} line={line} onClose={onClose} onSaved={onSaved} />
  </Dialog>
);
