import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useForm, useFieldArray, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Alert, Box, Button, Menu, MenuItem, Paper, Stack, Typography } from '@mui/material';
import { Add as AddIcon, Cancel as CancelIcon, Save as SaveIcon } from '@mui/icons-material';
import { FormSelect, FormTextField } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { usePermissions } from '@/hooks/usePermissions';
import { useSnackbar } from '@/hooks/useSnackbar';
import { usersApi } from '@/api/endpoints/users';
import { formsApi, type CreateFormData, type FormFieldType } from '@/api/endpoints/forms';
import {
  arrayError,
  emptyForm,
  fieldTypes,
  formSchema,
  newField,
  statusOptions,
  toFormValues,
  toPayload,
  type FormBuilderValues,
} from './formBuilderSchema';
import { FieldEditor } from './FieldEditor';
import { FormSettingsSections } from './FormSettingsSections';
import { SubmissionActionSection } from './SubmissionActionSection';

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { can } = usePermissions();
  const { showSuccess, showError } = useSnackbar();

  const isEditMode = Boolean(id);
  // A caller without forms.manage reads forms but never writes them; the API
  // enforces the same.
  const canManage = can('forms.manage');
  // The default-owner picker lists the users through GET /users.
  const canListUsers = can('users.view');
  const readOnly = !canManage;

  const [typeMenuAnchor, setTypeMenuAnchor] = useState<null | HTMLElement>(null);
  // Machine names follow the label until someone edits them, keyed by the
  // stable field id handed out by useFieldArray.
  const [manualNames, setManualNames] = useState<Record<string, boolean>>({});

  const methods = useForm<FormBuilderValues>({
    resolver: zodResolver(formSchema),
    defaultValues: emptyForm(),
  });
  const { control, handleSubmit, reset, setValue, formState } = methods;
  const fieldArray = useFieldArray({ control, name: 'fields' });

  const { data: form, isLoading } = useQuery({
    queryKey: ['form', id],
    queryFn: () => formsApi.get(Number(id)),
    enabled: isEditMode,
  });

  // Without users.view the list is not asked for; such a caller owns what it
  // creates.
  const { data: userPage } = useQuery({
    queryKey: ['users', { limit: 100 }],
    queryFn: () => usersApi.getUsers({ limit: 100 }),
    enabled: canListUsers,
  });

  useEffect(() => {
    if (form) {
      reset(toFormValues(form));
      setManualNames({});
    }
  }, [form, reset]);

  // A caller that cannot pick from the user list gets its own account as the
  // only sensible default for a brand-new form.
  useEffect(() => {
    if (!isEditMode && user && !canListUsers) {
      setValue('default_owner_id', user.id);
    }
  }, [isEditMode, user, canListUsers, setValue]);

  const createMutation = useMutation({
    mutationFn: (data: CreateFormData) => formsApi.create(data),
    onSuccess: (created) => {
      showSuccess('Form created successfully');
      queryClient.invalidateQueries({ queryKey: ['forms'] });
      navigate(`/forms/${created.id}`);
    },
    onError: () => {
      showError('Failed to create form');
    },
  });

  const updateMutation = useMutation({
    mutationFn: (data: CreateFormData) => formsApi.update(Number(id), data),
    onSuccess: () => {
      showSuccess('Form updated successfully');
      queryClient.invalidateQueries({ queryKey: ['forms'] });
      queryClient.invalidateQueries({ queryKey: ['form', id] });
      navigate(`/forms/${id}`);
    },
    onError: () => {
      showError('Failed to update form');
    },
  });

  const onSubmit = (values: FormBuilderValues) => {
    const payload = toPayload(values);
    if (isEditMode) {
      updateMutation.mutate(payload);
      return;
    }
    createMutation.mutate(payload);
  };

  const addField = (type: FormFieldType) => {
    fieldArray.append(newField(type));
    setTypeMenuAnchor(null);
  };

  if (isEditMode && isLoading) {
    return <Loading />;
  }

  // A cross-field issue on an array lands on the array itself — as `root` once
  // the array has entries — rather than on one of them, where react-hook-form's
  // typing expects a list.
  const fieldsError = arrayError(formState.errors.fields);

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">{isEditMode ? 'Edit Form' : 'Create Form'}</Typography>
      </Box>

      {readOnly && (
        <Alert severity="info" sx={{ mb: 3 }}>
          You have read-only access to forms. Ask an admin or a sales user to make changes.
        </Alert>
      )}

      <FormProvider {...methods}>
        <form onSubmit={handleSubmit(onSubmit)} noValidate>
          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              Basics
            </Typography>
            <Stack spacing={3} mt={1}>
              <FormTextField
                name="name"
                label="Name"
                required
                disabled={readOnly}
                helperText="Shown to your team and used as the source on created leads."
              />
              <FormTextField
                name="description"
                label="Description"
                multiline
                minRows={2}
                disabled={readOnly}
              />
              <FormSelect
                name="status"
                label="Status"
                options={statusOptions}
                disabled={readOnly}
              />
              <Typography variant="body2" color="text.secondary">
                Only a published form is served publicly.
              </Typography>
            </Stack>
          </Paper>

          <Paper sx={{ p: 3, mb: 3 }}>
            <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
              <Typography variant="h6">Fields</Typography>
              {!readOnly && (
                <>
                  <Button
                    startIcon={<AddIcon />}
                    onClick={(event) => setTypeMenuAnchor(event.currentTarget)}
                  >
                    Add field
                  </Button>
                  <Menu
                    anchorEl={typeMenuAnchor}
                    open={Boolean(typeMenuAnchor)}
                    onClose={() => setTypeMenuAnchor(null)}
                  >
                    {fieldTypes.map((type) => (
                      <MenuItem key={type.value} onClick={() => addField(type.value)}>
                        {type.label}
                      </MenuItem>
                    ))}
                  </Menu>
                </>
              )}
            </Box>

            {fieldsError && (
              <Alert severity="error" sx={{ mb: 2 }}>
                {fieldsError}
              </Alert>
            )}

            {fieldArray.fields.length === 0 ? (
              <Typography variant="body2" color="text.secondary">
                No fields yet.
              </Typography>
            ) : (
              <Stack spacing={2}>
                {fieldArray.fields.map((field, index) => (
                  <FieldEditor
                    key={field.id}
                    index={index}
                    total={fieldArray.fields.length}
                    disabled={readOnly}
                    autoName={!manualNames[field.id]}
                    onManualName={() =>
                      setManualNames((previous) => ({ ...previous, [field.id]: true }))
                    }
                    onMove={fieldArray.move}
                    onRemove={fieldArray.remove}
                  />
                ))}
              </Stack>
            )}
          </Paper>

          <FormSettingsSections readOnly={readOnly} />

          <SubmissionActionSection readOnly={readOnly} userList={userPage?.data} user={user} />

          <Box display="flex" gap={2} justifyContent="flex-end">
            <Button
              variant="outlined"
              startIcon={<CancelIcon />}
              onClick={() => navigate(isEditMode ? `/forms/${id}` : '/forms')}
            >
              Cancel
            </Button>
            {!readOnly && (
              <Button
                type="submit"
                variant="contained"
                startIcon={<SaveIcon />}
                disabled={createMutation.isPending || updateMutation.isPending}
              >
                {isEditMode ? 'Update Form' : 'Create Form'}
              </Button>
            )}
          </Box>
        </form>
      </FormProvider>
    </Box>
  );
};

Component.displayName = 'FormBuilder';
