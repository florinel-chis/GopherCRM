import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useForm, FormProvider, Controller } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Box, Paper, Typography, Button, Stack, TextField as MuiTextField } from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormSwitch, FormTextField } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { productsApi, type CreateProductData } from '@/api/endpoints';
import { centsToDecimal, decimalToCents } from '@/pages/deals/dealFormat';
import { useDefaultDealCurrency } from '@/pages/deals/useDefaultDealCurrency';
import { getAPIError } from '@/utils/apiError';

const PRICE_PATTERN = /^\d+(\.\d{1,2})?$/;
// The API refuses unit_price_cents above 10^12 (models.QuoteAmountCentsMax),
// far below 2^53, so the cents compare exactly as a plain number.
const PRICE_CENTS_MAX = 1_000_000_000_000;

// Bounds mirror the API's column sizes (name 200, SKU 64); the price is
// edited in major units and sent as integer cents.
const productSchema = z.object({
  name: z.string().trim().min(1, 'Name is required').max(200, 'Name must be 200 characters or fewer'),
  sku: z.string().trim().max(64, 'SKU must be 64 characters or fewer'),
  description: z.string(),
  price: z
    .string()
    .trim()
    .refine((value) => PRICE_PATTERN.test(value), {
      message: 'Price must be a non-negative number with at most two decimals',
    })
    .refine((value) => !PRICE_PATTERN.test(value) || decimalToCents(value) <= PRICE_CENTS_MAX, {
      message: 'Price is too large',
    }),
  currency: z
    .string()
    .trim()
    .toUpperCase()
    .regex(/^[A-Z]{3}$/, 'Currency must be a 3-letter ISO code'),
  is_active: z.boolean(),
});

type ProductFormData = z.infer<typeof productSchema>;

const emptyValues: ProductFormData = {
  name: '',
  sku: '',
  description: '',
  price: '',
  currency: 'EUR',
  is_active: true,
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;

  // The currency follows the configured default until the user edits it;
  // an existing product keeps its own.
  const [currencyTouched, setCurrencyTouched] = useState(false);
  const defaultCurrency = useDefaultDealCurrency(!isEditMode);

  const methods = useForm<ProductFormData>({
    resolver: zodResolver(productSchema),
    defaultValues: emptyValues,
  });
  const { control, setValue } = methods;

  const { data: product, isLoading } = useQuery({
    queryKey: ['product', id],
    queryFn: () => productsApi.getProduct(Number(id)),
    enabled: isEditMode,
  });

  useEffect(() => {
    if (!isEditMode && !currencyTouched) {
      setValue('currency', defaultCurrency);
    }
  }, [defaultCurrency, isEditMode, currencyTouched, setValue]);

  useEffect(() => {
    if (product) {
      methods.reset({
        name: product.name,
        sku: product.sku,
        description: product.description,
        price: centsToDecimal(product.unit_price_cents),
        currency: product.currency,
        is_active: product.is_active,
      });
    }
  }, [product, methods]);

  const reportError = (error: unknown, fallback: string) => {
    const message = getAPIError(error)?.message;
    showError(message || fallback);
  };

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['products'] });
    queryClient.invalidateQueries({ queryKey: ['product', id] });
  };

  const createMutation = useMutation({
    mutationFn: (data: CreateProductData) => productsApi.createProduct(data),
    onSuccess: () => {
      showSuccess('Product created successfully');
      invalidate();
      navigate('/products');
    },
    onError: (error) => reportError(error, 'Failed to create product'),
  });

  const updateMutation = useMutation({
    mutationFn: (data: CreateProductData) => productsApi.updateProduct(Number(id), data),
    onSuccess: () => {
      showSuccess('Product updated successfully');
      invalidate();
      navigate('/products');
    },
    onError: (error) => reportError(error, 'Failed to update product'),
  });

  const onSubmit = (data: ProductFormData) => {
    const payload: CreateProductData = {
      name: data.name,
      sku: data.sku,
      description: data.description,
      unit_price_cents: decimalToCents(data.price),
      currency: data.currency,
      is_active: data.is_active,
    };
    if (isEditMode) {
      updateMutation.mutate(payload);
    } else {
      createMutation.mutate(payload);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">{isEditMode ? 'Edit Product' : 'Create New Product'}</Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)} noValidate>
            <Stack spacing={3}>
              <Typography variant="h6">Product</Typography>

              <FormTextField name="name" label="Name" required />
              <FormTextField name="sku" label="SKU" helperText="Optional stock-keeping code" />
              <FormTextField name="description" label="Description" multiline rows={3} />

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '2fr 1fr' }} gap={2}>
                <FormTextField
                  name="price"
                  label="Price"
                  placeholder="0.00"
                  required
                  inputProps={{ inputMode: 'decimal' }}
                  helperText="Decimal price, stored in minor units"
                />
                <Controller
                  name="currency"
                  control={control}
                  render={({ field, fieldState: { error } }) => (
                    <MuiTextField
                      {...field}
                      onChange={(event) => {
                        setCurrencyTouched(true);
                        field.onChange(event.target.value.toUpperCase());
                      }}
                      label="Currency"
                      inputProps={{ maxLength: 3, style: { textTransform: 'uppercase' } }}
                      error={!!error}
                      helperText={error?.message || 'ISO 4217 code'}
                      fullWidth
                    />
                  )}
                />
              </Box>

              <FormSwitch name="is_active" label="Active" />

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button variant="outlined" startIcon={<CancelIcon />} onClick={() => navigate('/products')}>
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Product
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};
