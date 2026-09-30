import React, { useState } from 'react';
import { useParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  Divider,
  Stack,
  IconButton,
  Link,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TablePagination,
  TableRow,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  Phone as PhoneIcon,
  Language as WebIcon,
  Business as BusinessIcon,
  Person as PersonIcon,
  CalendarToday as CalendarIcon,
  Group as GroupIcon,
  LocationOn as LocationIcon,
  Notes as NotesIcon,
  PersonAdd as PersonAddIcon,
  Public as DomainIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { companiesApi } from '@/api/endpoints';
import { formatDate } from '@/utils/date';

const SUB_TABLE_ROWS_PER_PAGE = [5, 10, 25];

interface FieldProps {
  icon: React.ReactNode;
  label: string;
  children: React.ReactNode;
}

const Field: React.FC<FieldProps> = ({ icon, label, children }) => (
  <Box display="flex" alignItems="center" gap={1}>
    {icon}
    <Box>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
      <Typography component="div">{children}</Typography>
    </Box>
  </Box>
);

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  const companyId = Number(id);
  const canWrite = user?.role === 'admin' || user?.role === 'sales';
  const canDelete = user?.role === 'admin';
  // GET /companies/:id/leads is admin+sales; support has no leads access at
  // all, so the section is not rendered rather than shown as a 403.
  const canSeeLeads = user?.role === 'admin' || user?.role === 'sales';

  const [deleteDialog, setDeleteDialog] = useState(false);
  const [customersPage, setCustomersPage] = useState({ page: 0, limit: 5 });
  const [leadsPage, setLeadsPage] = useState({ page: 0, limit: 5 });

  const { data: company, isLoading } = useQuery({
    queryKey: ['company', id],
    queryFn: () => companiesApi.getCompany(companyId),
    enabled: !!id,
  });

  const { data: customers } = useQuery({
    queryKey: ['company', id, 'customers', customersPage],
    queryFn: () =>
      companiesApi.getCompanyCustomers(companyId, {
        offset: customersPage.page * customersPage.limit,
        limit: customersPage.limit,
      }),
    enabled: !!id,
  });

  const { data: leads } = useQuery({
    queryKey: ['company', id, 'leads', leadsPage],
    queryFn: () =>
      companiesApi.getCompanyLeads(companyId, {
        offset: leadsPage.page * leadsPage.limit,
        limit: leadsPage.limit,
      }),
    enabled: !!id && canSeeLeads,
  });

  const deleteMutation = useMutation({
    mutationFn: () => companiesApi.deleteCompany(companyId),
    onSuccess: () => {
      showSuccess('Company deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['companies'] });
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      queryClient.invalidateQueries({ queryKey: ['customer'] });
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      queryClient.invalidateQueries({ queryKey: ['lead'] });
      navigate('/companies');
    },
    onError: () => {
      showError('Failed to delete company');
    },
  });

  if (isLoading || !company) {
    return <Loading />;
  }

  const hasAddress = company.address || company.city || company.country || company.postal_code;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">{company.name}</Typography>
          {company.employee_range && <Chip label={`${company.employee_range} employees`} />}
        </Box>
        <Box display="flex" gap={1}>
          {canWrite && (
            <Button
              variant="outlined"
              startIcon={<EditIcon />}
              onClick={() => navigate(`/companies/${id}/edit`)}
            >
              Edit
            </Button>
          )}
          {canDelete && (
            <IconButton
              color="error"
              aria-label="Delete company"
              onClick={() => setDeleteDialog(true)}
            >
              <DeleteIcon />
            </IconButton>
          )}
        </Box>
      </Box>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Stack spacing={3}>
          <Box>
            <Typography variant="h6" gutterBottom>
              Company Information
            </Typography>
            <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
              <Field icon={<DomainIcon color="action" />} label="Domain">
                {company.domain || 'Not specified'}
              </Field>
              <Field icon={<WebIcon color="action" />} label="Website">
                {company.website ? (
                  <Link href={company.website} target="_blank" rel="noopener noreferrer">
                    {company.website}
                  </Link>
                ) : (
                  'Not specified'
                )}
              </Field>
              <Field icon={<BusinessIcon color="action" />} label="Industry">
                {company.industry || 'Not specified'}
              </Field>
              <Field icon={<GroupIcon color="action" />} label="Employees">
                {company.employee_range || 'Not specified'}
              </Field>
              <Field icon={<PhoneIcon color="action" />} label="Phone">
                {company.phone ? <Link href={`tel:${company.phone}`}>{company.phone}</Link> : 'Not specified'}
              </Field>
              <Field icon={<PersonIcon color="action" />} label="Owner">
                {company.owner
                  ? `${company.owner.first_name} ${company.owner.last_name}`
                  : 'Unassigned'}
              </Field>
              <Field icon={<CalendarIcon color="action" />} label="Created">
                {formatDate(company.created_at, 'MMM dd, yyyy HH:mm')}
              </Field>
              <Field icon={<CalendarIcon color="action" />} label="Last Updated">
                {formatDate(company.updated_at, 'MMM dd, yyyy HH:mm')}
              </Field>
            </Box>
          </Box>

          {hasAddress && (
            <>
              <Divider />
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={2}>
                  <LocationIcon color="action" />
                  <Typography variant="h6">Address</Typography>
                </Box>
                <Typography>
                  {company.address && (
                    <>
                      {company.address}
                      <br />
                    </>
                  )}
                  {company.city && `${company.city}${company.state ? `, ${company.state}` : ''}`}
                  {company.postal_code && ` ${company.postal_code}`}
                  {company.country && (
                    <>
                      <br />
                      {company.country}
                    </>
                  )}
                </Typography>
              </Box>
            </>
          )}

          {company.notes && (
            <>
              <Divider />
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={1}>
                  <NotesIcon color="action" />
                  <Typography variant="h6">Notes</Typography>
                </Box>
                <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
                  {company.notes}
                </Typography>
              </Box>
            </>
          )}
        </Stack>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }} component="section" aria-labelledby="company-customers-heading">
        <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
          <Typography variant="h6" id="company-customers-heading">
            Customers{company.customer_count !== undefined && ` (${company.customer_count})`}
          </Typography>
          {canWrite && (
            <Button
              variant="outlined"
              size="small"
              startIcon={<PersonAddIcon />}
              onClick={() => navigate(`/customers/new?company_id=${company.id}`)}
            >
              New Customer
            </Button>
          )}
        </Box>
        {customers && customers.customers.length > 0 ? (
          <>
            <Table size="small" aria-label="Customers of this company">
              <TableHead>
                <TableRow>
                  <TableCell>Contact</TableCell>
                  <TableCell>Email</TableCell>
                  <TableCell>Phone</TableCell>
                  <TableCell>Since</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {customers.customers.map((customer) => (
                  <TableRow key={customer.id} hover>
                    <TableCell>
                      <Link component={RouterLink} to={`/customers/${customer.id}`}>
                        {customer.contact_name || customer.email}
                      </Link>
                    </TableCell>
                    <TableCell>{customer.email}</TableCell>
                    <TableCell>{customer.phone}</TableCell>
                    <TableCell>{formatDate(customer.created_at, 'MMM dd, yyyy')}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <TablePagination
              component="div"
              count={customers.total}
              page={customersPage.page}
              rowsPerPage={customersPage.limit}
              rowsPerPageOptions={SUB_TABLE_ROWS_PER_PAGE}
              onPageChange={(_, page) => setCustomersPage((prev) => ({ ...prev, page }))}
              onRowsPerPageChange={(event) =>
                setCustomersPage({ page: 0, limit: parseInt(event.target.value, 10) })
              }
            />
          </>
        ) : (
          <Typography color="text.secondary">No customers linked to this company</Typography>
        )}
      </Paper>

      {canSeeLeads && (
        <Paper sx={{ p: 3 }} component="section" aria-labelledby="company-leads-heading">
          <Typography variant="h6" id="company-leads-heading" mb={2}>
            Leads{company.lead_count !== undefined && ` (${company.lead_count})`}
          </Typography>
          {leads && leads.leads.length > 0 ? (
            <>
              <Table size="small" aria-label="Leads of this company">
                <TableHead>
                  <TableRow>
                    <TableCell>Contact</TableCell>
                    <TableCell>Email</TableCell>
                    <TableCell>Status</TableCell>
                    <TableCell>Created</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {leads.leads.map((lead) => (
                    <TableRow key={lead.id} hover>
                      <TableCell>
                        <Link component={RouterLink} to={`/leads/${lead.id}`}>
                          {lead.contact_name || lead.email}
                        </Link>
                      </TableCell>
                      <TableCell>{lead.email}</TableCell>
                      <TableCell>{lead.status}</TableCell>
                      <TableCell>{formatDate(lead.created_at, 'MMM dd, yyyy')}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <TablePagination
                component="div"
                count={leads.total}
                page={leadsPage.page}
                rowsPerPage={leadsPage.limit}
                rowsPerPageOptions={SUB_TABLE_ROWS_PER_PAGE}
                onPageChange={(_, page) => setLeadsPage((prev) => ({ ...prev, page }))}
                onRowsPerPageChange={(event) =>
                  setLeadsPage({ page: 0, limit: parseInt(event.target.value, 10) })
                }
              />
            </>
          ) : (
            <Typography color="text.secondary">No leads linked to this company</Typography>
          )}
        </Paper>
      )}

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Company"
        message={`Are you sure you want to delete "${company.name}"? Its leads and customers keep their records but lose the link. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};
