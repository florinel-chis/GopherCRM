import React from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Typography,
  Box,
  Alert,
} from '@mui/material';
import { useForm, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { FormTextField } from '@/components/form';
import type { Lead } from '@/types';

const convertLeadSchema = z.object({
  company_name: z.string().optional(),
  website: z.string().url('Invalid URL format').optional().or(z.literal('')),
  address: z.string().optional(),
  notes: z.string().optional(),
});

type ConvertLeadFormData = z.infer<typeof convertLeadSchema>;

export interface ConvertLeadData {
  company_name?: string;
  website?: string;
  address?: string;
  notes?: string;
}

interface ConvertLeadDialogProps {
  open: boolean;
  lead: Lead | null;
  onClose: () => void;
  onConfirm: (data: ConvertLeadData) => void;
  isLoading?: boolean;
}

export const ConvertLeadDialog: React.FC<ConvertLeadDialogProps> = ({
  open,
  lead,
  onClose,
  onConfirm,
  isLoading = false,
}) => {
  const methods = useForm<ConvertLeadFormData>({
    resolver: zodResolver(convertLeadSchema),
    defaultValues: {
      company_name: lead?.company_name || '',
      website: '',
      address: '',
      notes: '',
    },
  });

  // Reset form when lead changes
  React.useEffect(() => {
    if (lead) {
      methods.reset({
        company_name: lead.company_name || '',
        website: '',
        address: '',
        notes: '',
      });
    }
  }, [lead, methods]);

  const handleSubmit = (data: ConvertLeadFormData) => {
    // Filter out empty strings
    const filteredData: ConvertLeadData = {};
    if (data.company_name?.trim()) filteredData.company_name = data.company_name.trim();
    if (data.website?.trim()) filteredData.website = data.website.trim();
    if (data.address?.trim()) filteredData.address = data.address.trim();
    if (data.notes?.trim()) filteredData.notes = data.notes.trim();
    
    onConfirm(filteredData);
  };

  const handleClose = () => {
    methods.reset();
    onClose();
  };

  if (!lead) return null;

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="md" fullWidth>
      <DialogTitle>
        Convert Lead to Customer
      </DialogTitle>
      
      <DialogContent>
        <Box sx={{ mb: 3 }}>
          <Alert severity="info" sx={{ mb: 2 }}>
            Converting this lead will create a new customer record. The lead will be marked as "converted" and linked to the new customer.
          </Alert>
          
          <Typography variant="h6" gutterBottom>
            Lead Information
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            <strong>Contact:</strong> {lead.contact_name}<br />
            <strong>Email:</strong> {lead.email}<br />
            <strong>Phone:</strong> {lead.phone}<br />
            <strong>Current Company:</strong> {lead.company_name || 'Not specified'}
          </Typography>
        </Box>

        <FormProvider {...methods}>
          <Box component="form" onSubmit={methods.handleSubmit(handleSubmit)}>
            <Typography variant="h6" gutterBottom>
              Additional Customer Information (Optional)
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              You can update or add additional information for the new customer record.
            </Typography>
            
            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <FormTextField
                name="company_name"
                label="Company Name"
                placeholder="Leave empty to use current company name"
                fullWidth
              />
              
              <FormTextField
                name="website"
                label="Website"
                placeholder="https://example.com"
                fullWidth
              />
              
              <FormTextField
                name="address"
                label="Address"
                placeholder="Company address"
                multiline
                rows={2}
                fullWidth
              />
              
              <FormTextField
                name="notes"
                label="Conversion Notes"
                placeholder="Add any notes about this conversion..."
                multiline
                rows={3}
                fullWidth
              />
            </Box>
          </Box>
        </FormProvider>
      </DialogContent>
      
      <DialogActions>
        <Button onClick={handleClose} disabled={isLoading}>
          Cancel
        </Button>
        <Button
          onClick={methods.handleSubmit(handleSubmit)}
          variant="contained"
          color="primary"
          disabled={isLoading}
        >
          {isLoading ? 'Converting...' : 'Convert to Customer'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};