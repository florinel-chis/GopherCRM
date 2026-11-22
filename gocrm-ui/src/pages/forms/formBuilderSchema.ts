import * as z from 'zod';
import type { CreateFormData, Form, FormFieldType } from '@/api/endpoints/forms';

export const fieldTypes: { value: FormFieldType; label: string }[] = [
  { value: 'text', label: 'Text' },
  { value: 'email', label: 'Email' },
  { value: 'phone', label: 'Phone' },
  { value: 'textarea', label: 'Long text' },
  { value: 'select', label: 'Dropdown' },
  { value: 'checkbox', label: 'Checkbox' },
  { value: 'hidden', label: 'Hidden' },
];

export const statusOptions = [
  { value: 'draft', label: 'Draft' },
  { value: 'published', label: 'Published' },
  { value: 'archived', label: 'Archived' },
];

export const fieldNamePattern = /^[a-z][a-z0-9_]{0,49}$/;
export const emailPattern = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
export const hostPattern = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

// Long text gets a roomier default; the server applies the same numbers and
// caps anything above 10000.
export const defaultMaxLength = (type: FormFieldType) => (type === 'textarea' ? 5000 : 1000);

// The schema mirrors the server-side definition validation so a form that
// passes here is not bounced by the API.
export const fieldSchema = z
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

export const formSchema = z
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
    create_ticket: z.boolean(),
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

    // The server's ticket rules: a ticket form creates no lead and only opens a
    // ticket for a confirmed address.
    if (values.create_ticket && values.create_lead) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['create_ticket'],
        message: 'A form cannot both create leads and create tickets',
      });
    }

    if (values.create_ticket && !values.double_opt_in) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['double_opt_in'],
        message: 'A form that creates tickets needs double opt-in',
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

export type FormBuilderValues = z.infer<typeof formSchema>;
export type FieldValues = FormBuilderValues['fields'][number];

export const newField = (type: FormFieldType): FieldValues => ({
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
export const emptyForm = (): FormBuilderValues => ({
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
  create_ticket: false,
  default_owner_id: 0,
  allowed_domains: [],
});

export const toFormValues = (form: Form): FormBuilderValues => ({
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
  create_lead: form.create_lead ?? !form.create_ticket,
  create_ticket: Boolean(form.create_ticket),
  default_owner_id: form.default_owner_id ?? 0,
  allowed_domains: form.allowed_domains ?? [],
});

export const toPayload = (values: FormBuilderValues): CreateFormData => ({
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
  // Always sent: PUT replaces the whole definition and an omitted flag is
  // stored false, which would silently turn ticket intake off.
  create_ticket: values.create_ticket,
  default_owner_id: values.create_lead ? values.default_owner_id : 0,
  allowed_domains: values.allowed_domains.map((domain) => domain.trim().toLowerCase()),
});

// What a submission creates. The form stores two flags; the builder offers one
// choice so the excluded combination cannot be picked.
export type SubmissionAction = 'lead' | 'ticket' | 'none';

export const toSubmissionAction = (createLead: boolean, createTicket: boolean): SubmissionAction => {
  if (createTicket) {
    return 'ticket';
  }
  return createLead ? 'lead' : 'none';
};

export const isSubmissionAction = (value: string): value is SubmissionAction =>
  value === 'lead' || value === 'ticket' || value === 'none';

// Reads the message of an issue raised against a whole array rather than one of
// its entries. react-hook-form parks it on `root` once the array has entries and
// on the array node itself when it is empty.
export type ArrayFieldError = { message?: string; root?: { message?: string } } | undefined;

export const arrayError = (error: unknown): string | undefined => {
  const arrayFieldError = error as ArrayFieldError;
  return arrayFieldError?.root?.message || arrayFieldError?.message;
};

// Machine name suggested from the label, matching ^[a-z][a-z0-9_]{0,49}$.
export const slugify = (label: string): string =>
  label
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^[^a-z]+/, '')
    .replace(/_+$/, '')
    .slice(0, 50);
