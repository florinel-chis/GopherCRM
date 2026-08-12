import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';

// CRM-defined web forms served to external sites. Every shape below mirrors the
// Go transport DTOs field-for-field (snake_case preserved); the axios client
// already peels the { success, data } envelope, so `response.data` is the
// payload itself and the list totals arrive on `response.meta`.

export type FormFieldType =
  | 'text'
  | 'email'
  | 'phone'
  | 'textarea'
  | 'select'
  | 'checkbox'
  | 'hidden';

export type FormStatus = 'draft' | 'published' | 'archived';

export type FormSubmitAction = 'message' | 'redirect';

export type FormSubmissionStatus = 'received' | 'pending' | 'confirmed' | 'spam';

export interface FormFieldDef {
  name: string;
  label: string;
  type: FormFieldType;
  required: boolean;
  placeholder?: string;
  help_text?: string;
  options?: string[];
  max_length?: number;
}

export interface Form {
  id: number;
  name: string;
  description: string;
  public_id: string;
  status: FormStatus;
  fields: FormFieldDef[];
  submit_action: FormSubmitAction;
  thank_you_message: string;
  redirect_url: string;
  consent_text: string;
  notify_emails: string[];
  double_opt_in: boolean;
  confirmation_subject: string;
  confirmation_body: string;
  follow_up_subject: string;
  follow_up_body: string;
  content_url: string;
  captcha_enabled: boolean;
  create_lead: boolean;
  default_owner_id: number;
  allowed_domains: string[];
  created_at: string;
  updated_at: string;
  submission_count?: number;
}

// The public id is minted server-side and immutable; the timestamps and the
// submission count are read-only projections.
export type CreateFormData = Omit<
  Form,
  'id' | 'public_id' | 'created_at' | 'updated_at' | 'submission_count'
>;

// PUT replaces the whole definition, so an update carries the same payload.
export type UpdateFormData = CreateFormData;

export interface FormSubmission {
  id: number;
  form_id: number;
  data: Record<string, string>;
  email: string;
  status: FormSubmissionStatus;
  spam_reason: string;
  lead_id: number | null;
  ip_address: string;
  user_agent: string;
  referrer: string;
  confirmed_at: string | null;
  created_at: string;
}

export interface ListFormsParams {
  offset?: number;
  limit?: number;
  status?: string;
  sort_by?: string;
  sort_order?: 'asc' | 'desc';
}

export interface FormListResult {
  forms: Form[];
  total: number;
}

export interface ListFormSubmissionsParams {
  offset?: number;
  limit?: number;
  status?: string;
}

export interface FormSubmissionListResult {
  submissions: FormSubmission[];
  total: number;
}

export const formsApi = {
  list: async (params?: ListFormsParams): Promise<FormListResult> => {
    const response = (await api.get<Form[]>('/forms', { params })) as ApiResponseWithMeta<Form[]>;
    const forms = Array.isArray(response.data) ? response.data : [];
    // A page shorter than the total is the norm; fall back to the page length
    // only when the server sent no meta block at all.
    return { forms, total: response.meta?.total ?? forms.length };
  },

  get: async (id: number): Promise<Form> => {
    const response = await api.get<Form>(`/forms/${id}`);
    return response.data;
  },

  create: async (data: CreateFormData): Promise<Form> => {
    const response = await api.post<Form>('/forms', data);
    return response.data;
  },

  update: async (id: number, data: UpdateFormData): Promise<Form> => {
    const response = await api.put<Form>(`/forms/${id}`, data);
    return response.data;
  },

  delete: async (id: number): Promise<void> => {
    await api.delete(`/forms/${id}`);
  },

  listSubmissions: async (
    formId: number,
    params?: ListFormSubmissionsParams
  ): Promise<FormSubmissionListResult> => {
    const response = (await api.get<FormSubmission[]>(`/forms/${formId}/submissions`, {
      params,
    })) as ApiResponseWithMeta<FormSubmission[]>;
    const submissions = Array.isArray(response.data) ? response.data : [];
    return { submissions, total: response.meta?.total ?? submissions.length };
  },

  getSubmission: async (id: number): Promise<FormSubmission> => {
    const response = await api.get<FormSubmission>(`/forms/submissions/${id}`);
    return response.data;
  },
};
