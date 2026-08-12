import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useForm, useFieldArray, useFormContext, FormProvider, Controller } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Divider,
  FormControlLabel,
  FormHelperText,
  IconButton,
  Menu,
  MenuItem,
  Paper,
  Radio,
  RadioGroup,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  ArrowDownward as ArrowDownwardIcon,
  ArrowUpward as ArrowUpwardIcon,
  Cancel as CancelIcon,
  Delete as DeleteIcon,
  Save as SaveIcon,
} from '@mui/icons-material';
import { FormSelect, FormSwitch, FormTextField } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { usersApi } from '@/api/endpoints/users';
import {
  formsApi,
  type CreateFormData,
  type Form,
  type FormFieldType,
} from '@/api/endpoints/forms';

const fieldTypes: { value: FormFieldType; label: string }[] = [
  { value: 'text', label: 'Text' },
  { value: 'email', label: 'Email' },
  { value: 'phone', label: 'Phone' },
  { value: 'textarea', label: 'Long text' },
  { value: 'select', label: 'Dropdown' },
  { value: 'checkbox', label: 'Checkbox' },
  { value: 'hidden', label: 'Hidden' },
];

const statusOptions = [
  { value: 'draft', label: 'Draft' },
  { value: 'published', label: 'Published' },
  { value: 'archived', label: 'Archived' },
];

const fieldNamePattern = /^[a-z][a-z0-9_]{0,49}$/;
const emailPattern = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
const hostPattern = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

// Long text gets a roomier default; the server applies the same numbers and
// caps anything above 10000.
const defaultMaxLength = (type: FormFieldType) => (type === 'textarea' ? 5000 : 1000);

// The schema mirrors the server-side definition validation so a form that
// passes here is not bounced by the API.
const fieldSchema = z
  .object({
    name: z
      .string()
      .trim()
      .min(1, 'Field name is required')
      .regex(
        fieldNamePattern,
        'Use lowercase letters, digits and underscores, starting with a letter'
      ),
    label: z.string().trim().min(1, 'Label is required'),
    type: z.enum(['text', 'email', 'phone', 'textarea', 'select', 'checkbox', 'hidden']),
    required: z.boolean(),
    placeholder: z.string(),
    help_text: z.string(),
    options: z.array(z.string()),
    max_length: z.number().int().min(1).max(10000),
  })
  .superRefine((field, ctx) => {
    const options = field.options.map((option) => option.trim()).filter(Boolean);
    if (field.type === 'select' && options.length === 0) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['options'],
        message: 'A dropdown needs at least one option',
      });
    }
    if (field.type === 'select' && options.length > 50) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['options'],
        message: 'A dropdown takes at most 50 options',
      });
    }
    if (field.type === 'hidden' && field.required) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['required'],
        message: 'A hidden field cannot be required',
      });
    }
  });

const formSchema = z
  .object({
    name: z
      .string()
      .trim()
      .min(1, 'Name is required')
      .max(255, 'Name must be 255 characters or fewer'),
    description: z.string(),
    status: z.enum(['draft', 'published', 'archived']),
    fields: z
      .array(fieldSchema)
      .min(1, 'A form needs at least one field')
      .max(50, 'A form takes at most 50 fields'),
    submit_action: z.enum(['message', 'redirect']),
    thank_you_message: z.string(),
    redirect_url: z.string(),
    consent_text: z.string(),
    notify_emails: z.array(z.string()),
    double_opt_in: z.boolean(),
    confirmation_subject: z.string(),
    confirmation_body: z.string(),
    follow_up_subject: z.string(),
    follow_up_body: z.string(),
    content_url: z.string(),
    captcha_enabled: z.boolean(),
    create_lead: z.boolean(),
    default_owner_id: z.number(),
    allowed_domains: z.array(z.string()),
  })
  .superRefine((values, ctx) => {
    const names = values.fields.map((field) => field.name.trim());
    names.forEach((name, index) => {
      if (name && names.indexOf(name) !== index) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['fields', index, 'name'],
          message: 'Field names must be unique',
        });
      }
    });

    const emailFields = values.fields.filter((field) => field.type === 'email');
    if (emailFields.length !== 1) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['fields'],
        message: 'Add exactly one email field — every form collects an email address',
      });
    } else if (emailFields[0].name.trim() !== 'email') {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['fields'],
        message: 'The email field must be named "email"',
      });
    }

    if (values.submit_action === 'redirect' && !/^https?:\/\/\S+$/i.test(values.redirect_url.trim())) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['redirect_url'],
        message: 'A redirect needs an http(s) URL',
      });
    }

    if (values.create_lead && !values.default_owner_id) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['default_owner_id'],
        message: 'Choose the owner for the leads this form creates',
      });
    }

    values.notify_emails.forEach((email) => {
      if (!emailPattern.test(email.trim())) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['notify_emails'],
          message: `"${email}" is not a valid email address`,
        });
      }
    });

    values.allowed_domains.forEach((domain) => {
      if (!hostPattern.test(domain.trim().toLowerCase())) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['allowed_domains'],
          message: `"${domain}" must be a bare host such as example.com`,
        });
      }
    });
  });

type FormBuilderValues = z.infer<typeof formSchema>;
type FieldValues = FormBuilderValues['fields'][number];

const newField = (type: FormFieldType): FieldValues => ({
  name: type === 'email' ? 'email' : '',
  label: type === 'email' ? 'Email' : '',
  type,
  required: type === 'email',
  placeholder: '',
  help_text: '',
  options: [],
  max_length: defaultMaxLength(type),
});

// Every form collects an email address, so a new definition starts with the one
// field the server insists on.
const emptyForm = (): FormBuilderValues => ({
  name: '',
  description: '',
  status: 'draft',
  fields: [newField('email')],
  submit_action: 'message',
  thank_you_message: '',
  redirect_url: '',
  consent_text: '',
  notify_emails: [],
  double_opt_in: false,
  confirmation_subject: '',
  confirmation_body: '',
  follow_up_subject: '',
  follow_up_body: '',
  content_url: '',
  captcha_enabled: false,
  create_lead: true,
  default_owner_id: 0,
  allowed_domains: [],
});

const toFormValues = (form: Form): FormBuilderValues => ({
  name: form.name ?? '',
  description: form.description ?? '',
  status: form.status ?? 'draft',
  fields: (form.fields ?? []).map((field) => ({
    name: field.name ?? '',
    label: field.label ?? '',
    type: field.type,
    required: Boolean(field.required),
    placeholder: field.placeholder ?? '',
    help_text: field.help_text ?? '',
    options: field.options ?? [],
    max_length: field.max_length || defaultMaxLength(field.type),
  })),
  submit_action: form.submit_action ?? 'message',
  thank_you_message: form.thank_you_message ?? '',
  redirect_url: form.redirect_url ?? '',
  consent_text: form.consent_text ?? '',
  notify_emails: form.notify_emails ?? [],
  double_opt_in: Boolean(form.double_opt_in),
  confirmation_subject: form.confirmation_subject ?? '',
  confirmation_body: form.confirmation_body ?? '',
  follow_up_subject: form.follow_up_subject ?? '',
  follow_up_body: form.follow_up_body ?? '',
  content_url: form.content_url ?? '',
  captcha_enabled: Boolean(form.captcha_enabled),
  create_lead: form.create_lead ?? true,
  default_owner_id: form.default_owner_id ?? 0,
  allowed_domains: form.allowed_domains ?? [],
});

const toPayload = (values: FormBuilderValues): CreateFormData => ({
  name: values.name.trim(),
  description: values.description,
  status: values.status,
  fields: values.fields.map((field) => ({
    name: field.name.trim(),
    label: field.label.trim(),
    type: field.type,
    // The server rejects a required hidden field; the switch is hidden for that
    // type, so normalise rather than let a stale value through.
    required: field.type === 'hidden' ? false : field.required,
    placeholder: field.placeholder,
    help_text: field.help_text,
    // Options belong to dropdowns only.
    options:
      field.type === 'select'
        ? field.options.map((option) => option.trim()).filter(Boolean)
        : undefined,
    max_length: field.max_length,
  })),
  submit_action: values.submit_action,
  thank_you_message: values.thank_you_message,
  redirect_url: values.submit_action === 'redirect' ? values.redirect_url.trim() : '',
  consent_text: values.consent_text,
  notify_emails: values.notify_emails.map((email) => email.trim()),
  double_opt_in: values.double_opt_in,
  confirmation_subject: values.confirmation_subject,
  confirmation_body: values.confirmation_body,
  follow_up_subject: values.follow_up_subject,
  follow_up_body: values.follow_up_body,
  content_url: values.content_url.trim(),
  captcha_enabled: values.captcha_enabled,
  create_lead: values.create_lead,
  default_owner_id: values.create_lead ? values.default_owner_id : 0,
  allowed_domains: values.allowed_domains.map((domain) => domain.trim().toLowerCase()),
});

// Reads the message of an issue raised against a whole array rather than one of
// its entries. react-hook-form parks it on `root` once the array has entries and
// on the array node itself when it is empty.
type ArrayFieldError = { message?: string; root?: { message?: string } } | undefined;

const arrayError = (error: unknown): string | undefined => {
  const arrayFieldError = error as ArrayFieldError;
  return arrayFieldError?.root?.message || arrayFieldError?.message;
};

// Machine name suggested from the label, matching ^[a-z][a-z0-9_]{0,49}$.
const slugify = (label: string): string =>
  label
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^[^a-z]+/, '')
    .replace(/_+$/, '')
    .slice(0, 50);

interface StringListEditorProps {
  label: string;
  placeholder: string;
  values: string[];
  disabled?: boolean;
  error?: string;
  helperText?: string;
  onChange: (values: string[]) => void;
}

// Chip editor shared by the notification addresses, the allowed domains and the
// per-field dropdown options.
const StringListEditor: React.FC<StringListEditorProps> = ({
  label,
  placeholder,
  values,
  disabled,
  error,
  helperText,
  onChange,
}) => {
  const [draft, setDraft] = useState('');

  const commit = () => {
    const value = draft.trim();
    if (!value || values.includes(value)) {
      setDraft('');
      return;
    }
    onChange([...values, value]);
    setDraft('');
  };

  return (
    <Box>
      <Stack direction="row" spacing={1} mb={1}>
        <TextField
          size="small"
          fullWidth
          label={label}
          placeholder={placeholder}
          value={draft}
          disabled={disabled}
          error={Boolean(error)}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              commit();
            }
          }}
        />
        <Button variant="outlined" onClick={commit} disabled={disabled} aria-label={`Add ${label}`}>
          Add
        </Button>
      </Stack>
      <Box display="flex" gap={1} flexWrap="wrap">
        {values.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            None yet.
          </Typography>
        ) : (
          values.map((value) => (
            <Chip
              key={value}
              label={value}
              onDelete={disabled ? undefined : () => onChange(values.filter((v) => v !== value))}
            />
          ))
        )}
      </Box>
      {(error || helperText) && (
        <FormHelperText error={Boolean(error)}>{error || helperText}</FormHelperText>
      )}
    </Box>
  );
};

interface FieldEditorProps {
  index: number;
  total: number;
  disabled: boolean;
  autoName: boolean;
  onManualName: () => void;
  onMove: (from: number, to: number) => void;
  onRemove: (index: number) => void;
}

const FieldEditor: React.FC<FieldEditorProps> = ({
  index,
  total,
  disabled,
  autoName,
  onManualName,
  onMove,
  onRemove,
}) => {
  const { control, watch, setValue, getValues, formState } = useFormContext<FormBuilderValues>();
  const type = watch(`fields.${index}.type`);
  const options = watch(`fields.${index}.options`) || [];
  const optionsError = formState.errors.fields?.[index]?.options?.message;
  const position = index + 1;

  return (
    <Card variant="outlined" data-testid={`field-editor-${index}`}>
      <CardContent>
        <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
          <Typography variant="subtitle1">
            Field {position}: {fieldTypes.find((entry) => entry.value === type)?.label ?? type}
          </Typography>
          {!disabled && (
            <Box>
              <Tooltip title="Move up">
                <span>
                  <IconButton
                    size="small"
                    aria-label={`Move field ${position} up`}
                    disabled={index === 0}
                    onClick={() => onMove(index, index - 1)}
                  >
                    <ArrowUpwardIcon fontSize="small" />
                  </IconButton>
                </span>
              </Tooltip>
              <Tooltip title="Move down">
                <span>
                  <IconButton
                    size="small"
                    aria-label={`Move field ${position} down`}
                    disabled={index === total - 1}
                    onClick={() => onMove(index, index + 1)}
                  >
                    <ArrowDownwardIcon fontSize="small" />
                  </IconButton>
                </span>
              </Tooltip>
              <Tooltip title="Remove field">
                <IconButton
                  size="small"
                  aria-label={`Remove field ${position}`}
                  onClick={() => onRemove(index)}
                >
                  <DeleteIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            </Box>
          )}
        </Box>

        <Stack spacing={2}>
          <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
            <Controller
              name={`fields.${index}.label`}
              control={control}
              render={({ field, fieldState }) => (
                <TextField
                  {...field}
                  label={`Field ${position} label`}
                  required
                  fullWidth
                  disabled={disabled}
                  error={Boolean(fieldState.error)}
                  helperText={fieldState.error?.message}
                  onChange={(event) => {
                    const label = event.target.value;
                    field.onChange(label);
                    // The machine name tracks the label until it is edited by
                    // hand, then it is left alone. The email field is exempt:
                    // the server insists it is named "email".
                    if (autoName && getValues(`fields.${index}.type`) !== 'email') {
                      setValue(`fields.${index}.name`, slugify(label));
                    }
                  }}
                />
              )}
            />
            <Controller
              name={`fields.${index}.name`}
              control={control}
              render={({ field, fieldState }) => (
                <TextField
                  {...field}
                  label={`Field ${position} name`}
                  required
                  fullWidth
                  disabled={disabled}
                  error={Boolean(fieldState.error)}
                  helperText={
                    fieldState.error?.message ||
                    'Machine key stored with every submission; reserved names first_name, last_name, email, phone, company and position map onto the lead.'
                  }
                  onChange={(event) => {
                    onManualName();
                    field.onChange(event.target.value);
                  }}
                />
              )}
            />
          </Box>

          <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
            <FormTextField
              name={`fields.${index}.placeholder`}
              label={`Field ${position} placeholder`}
              disabled={disabled}
            />
            <FormTextField
              name={`fields.${index}.help_text`}
              label={`Field ${position} help text`}
              disabled={disabled}
            />
          </Box>

          {/* Hidden rather than unmounted: react-hook-form drops the value of an
              unmounted input at submit time, which turns a required schema key
              into a validation error with nothing on screen to fix. */}
          <Box sx={{ display: type === 'hidden' ? 'none' : 'block' }}>
            <FormSwitch
              name={`fields.${index}.required`}
              label={`Field ${position} required`}
              disabled={disabled}
            />
          </Box>

          {type === 'select' && (
            <StringListEditor
              label={`Field ${position} options`}
              placeholder="Under €10k"
              values={options}
              disabled={disabled}
              error={optionsError}
              onChange={(next) => setValue(`fields.${index}.options`, next, { shouldDirty: true })}
            />
          )}
        </Stack>
      </CardContent>
    </Card>
  );
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  const isEditMode = Boolean(id);
  // Support reads forms but never writes them; the API enforces the same.
  const canManage = user?.role === 'admin' || user?.role === 'sales';
  const readOnly = !canManage;

  const [typeMenuAnchor, setTypeMenuAnchor] = useState<null | HTMLElement>(null);
  // Machine names follow the label until someone edits them, keyed by the
  // stable field id handed out by useFieldArray.
  const [manualNames, setManualNames] = useState<Record<string, boolean>>({});

  const methods = useForm<FormBuilderValues>({
    resolver: zodResolver(formSchema),
    defaultValues: emptyForm(),
  });
  const { control, handleSubmit, reset, setValue, watch, formState } = methods;
  const fieldArray = useFieldArray({ control, name: 'fields' });

  const { data: form, isLoading } = useQuery({
    queryKey: ['form', id],
    queryFn: () => formsApi.get(Number(id)),
    enabled: isEditMode,
  });

  // Only admins may list users; a sales user owns what they create.
  const { data: userPage } = useQuery({
    queryKey: ['users', { limit: 100 }],
    queryFn: () => usersApi.getUsers({ limit: 100 }),
    enabled: user?.role === 'admin',
  });

  useEffect(() => {
    if (form) {
      reset(toFormValues(form));
      setManualNames({});
    }
  }, [form, reset]);

  // A sales user cannot pick from the user list, so their own account is the
  // only sensible default for a brand-new form.
  useEffect(() => {
    if (!isEditMode && user && user.role !== 'admin') {
      setValue('default_owner_id', user.id);
    }
  }, [isEditMode, user, setValue]);

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

  const submitAction = watch('submit_action');
  const doubleOptIn = watch('double_opt_in');
  const createLead = watch('create_lead');
  const notifyEmails = watch('notify_emails') || [];
  const allowedDomains = watch('allowed_domains') || [];
  // A cross-field issue on an array lands on the array itself — as `root` once
  // the array has entries — rather than on one of them, where react-hook-form's
  // typing expects a list.
  const fieldsError = arrayError(formState.errors.fields);
  const notifyEmailsError = arrayError(formState.errors.notify_emails);
  const allowedDomainsError = arrayError(formState.errors.allowed_domains);

  const ownerOptions = (() => {
    const users = (userPage?.data || []).filter(
      (candidate) =>
        candidate.is_active && (candidate.role === 'admin' || candidate.role === 'sales')
    );
    const options = users.map((candidate) => ({
      value: candidate.id,
      label: `${candidate.first_name} ${candidate.last_name} (${candidate.email})`,
    }));
    const selected = watch('default_owner_id');
    // Keep the stored owner selectable even when the list is unavailable (sales)
    // or the owner is filtered out of it.
    if (selected && !options.some((option) => option.value === selected)) {
      const label =
        user && user.id === selected
          ? `${user.first_name} ${user.last_name} (you)`
          : `User #${selected}`;
      options.unshift({ value: selected, label });
    }
    return options;
  })();

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

          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              After submission
            </Typography>
            <Controller
              name="submit_action"
              control={control}
              render={({ field }) => (
                <RadioGroup {...field} row>
                  <FormControlLabel
                    value="message"
                    control={<Radio disabled={readOnly} />}
                    label="Show a thank-you message"
                  />
                  <FormControlLabel
                    value="redirect"
                    control={<Radio disabled={readOnly} />}
                    label="Redirect to a URL"
                  />
                </RadioGroup>
              )}
            />
            <Stack spacing={3} mt={2}>
              {/* Both stay mounted: react-hook-form drops the value of an
                  unmounted input at submit time, and a missing required key
                  fails validation with nothing on screen to fix. */}
              <Box sx={{ display: submitAction === 'message' ? 'block' : 'none' }}>
                <FormTextField
                  name="thank_you_message"
                  label="Thank-you message"
                  multiline
                  minRows={2}
                  disabled={readOnly}
                  helperText="Left empty, the server uses its default wording."
                />
              </Box>
              <Box sx={{ display: submitAction === 'redirect' ? 'block' : 'none' }}>
                <FormTextField
                  name="redirect_url"
                  label="Redirect URL"
                  disabled={readOnly}
                  helperText="http(s) only."
                />
              </Box>
              <Divider />
              <FormTextField
                name="consent_text"
                label="Consent text"
                multiline
                minRows={2}
                disabled={readOnly}
                helperText="When set, the rendered form shows a required consent checkbox with this text."
              />
            </Stack>
          </Paper>

          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              Notifications
            </Typography>
            <Box mt={2}>
              <StringListEditor
                label="Notification recipients"
                placeholder="sales@example.com"
                values={notifyEmails}
                disabled={readOnly}
                error={notifyEmailsError}
                helperText="Notified on every non-spam submission."
                onChange={(next) => setValue('notify_emails', next, { shouldDirty: true })}
              />
            </Box>
          </Paper>

          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              Follow-up and double opt-in
            </Typography>
            <Stack spacing={3} mt={1}>
              <FormSwitch
                name="double_opt_in"
                label="Require email confirmation before the lead is created"
                disabled={readOnly}
              />
              <Box sx={{ display: doubleOptIn ? 'block' : 'none' }}>
                <Stack spacing={3}>
                  <FormTextField
                    name="confirmation_subject"
                    label="Confirmation subject"
                    disabled={readOnly}
                  />
                  <FormTextField
                    name="confirmation_body"
                    label="Confirmation body"
                    multiline
                    minRows={3}
                    disabled={readOnly}
                    helperText="Use {confirmation_link} where the confirmation link belongs. Defaults are used when left empty."
                  />
                </Stack>
              </Box>
              <Divider />
              <FormTextField
                name="follow_up_subject"
                label="Follow-up subject"
                disabled={readOnly}
                helperText={
                  doubleOptIn
                    ? 'Sent after the address is confirmed. Leave empty to send nothing.'
                    : 'Sent right after a submission. Leave empty to send nothing.'
                }
              />
              <FormTextField
                name="follow_up_body"
                label="Follow-up body"
                multiline
                minRows={3}
                disabled={readOnly}
                helperText="Use {content_link} where the gated-content link belongs."
              />
              <FormTextField
                name="content_url"
                label="Content URL"
                disabled={readOnly}
                helperText="Substituted for {content_link} in the follow-up email."
              />
            </Stack>
          </Paper>

          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              Spam protection
            </Typography>
            <Stack spacing={3} mt={1}>
              <Typography variant="body2" color="text.secondary">
                A honeypot field and a time trap are always active. reCAPTCHA is an extra layer.
              </Typography>
              <FormSwitch
                name="captcha_enabled"
                label="Verify submissions with reCAPTCHA"
                disabled={readOnly}
              />
              <Typography variant="body2" color="text.secondary">
                Requires reCAPTCHA keys in the server environment; without them the layer is
                skipped.
              </Typography>
              <StringListEditor
                label="Allowed domains"
                placeholder="example.com"
                values={allowedDomains}
                disabled={readOnly}
                error={allowedDomainsError}
                helperText="Bare hosts only. Empty means any site may embed this form; subdomains must be listed separately."
                onChange={(next) => setValue('allowed_domains', next, { shouldDirty: true })}
              />
            </Stack>
          </Paper>

          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              Lead capture
            </Typography>
            <Stack spacing={3} mt={1}>
              <FormSwitch
                name="create_lead"
                label="Create a lead from every submission"
                disabled={readOnly}
              />
              <Box sx={{ display: createLead ? 'block' : 'none' }}>
                <FormSelect
                  name="default_owner_id"
                  label="Lead owner"
                  options={ownerOptions}
                  disabled={readOnly}
                />
              </Box>
              <Typography variant="body2" color="text.secondary">
                A submission from a known email address is appended to the existing lead instead of
                creating a second one.
              </Typography>
            </Stack>
          </Paper>

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
