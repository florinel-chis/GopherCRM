import { AxiosError } from 'axios';
import { api } from '../client';

// Answer Engine Optimization. Every shape below mirrors the Go transport DTOs
// field-for-field (snake_case preserved); the axios client already peels the
// { success, data } envelope, so `response.data` is the payload itself.

export interface AEOCompetitor {
  name: string;
  aliases: string[];
  domain: string;
}

export interface AEOProfile {
  id: number;
  brand_name: string;
  description: string;
  brand_aliases: string[];
  owned_domains: string[];
  competitors: AEOCompetitor[];
}

export interface AEOPrompt {
  id: number;
  text: string;
  is_active: boolean;
  created_at: string;
  visibility: number;
  answer_count: number;
  mention_count: number;
  last_run_at?: string;
}

export interface AEORun {
  id: number;
  trigger: 'manual' | 'scheduled';
  status: 'running' | 'completed' | 'failed' | 'partial';
  started_at: string;
  completed_at?: string;
  total_queries: number;
  failed_queries: number;
}

export interface AEOCitation {
  id: number;
  answer_id: number;
  url: string;
  domain: string;
  is_owned: boolean;
  competitor_name?: string;
}

export interface AEOAnswer {
  id: number;
  run_id: number;
  prompt_id: number;
  provider: string;
  model: string;
  attempt: number;
  answer_text: string;
  brand_mentioned: boolean;
  first_mention_pos: number;
  competitor_mentions: Record<string, number>;
  latency_ms: number;
  error?: string;
  citations: AEOCitation[];
  created_at: string;
}

export interface AEOProviderStatus {
  name: string;
  model: string;
  configured: boolean;
}

export interface AEOProviderVisibility {
  provider: string;
  answers: number;
  mentions: number;
  visibility: number;
}

export interface AEOTimelinePoint {
  day: string;
  overall: number;
  by_provider: Record<string, number>;
}

export interface AEOCompetitorTimelinePoint {
  day: string;
  by_company: Record<string, number>;
}

export interface AEOShareOfVoiceEntry {
  company: string;
  is_brand: boolean;
  mentions: number;
  share: number;
  visibility: number;
}

export interface AEODashboard {
  from: string;
  to: string;
  days: number;
  total_answers: number;
  failed_answers: number;
  brand_mentions: number;
  visibility: number;
  by_provider: AEOProviderVisibility[];
  timeline: AEOTimelinePoint[];
  share_of_voice: AEOShareOfVoiceEntry[];
  competitor_timeline: AEOCompetitorTimelinePoint[];
  last_run_at?: string;
}

export interface AEOCitationCompanyStat {
  company: string;
  is_brand: boolean;
  citations: number;
  citation_rate: number;
  with_brand_mention: number;
  brand_mention_rate: number;
}

export interface AEOCitationDomainStat {
  domain: string;
  company: string;
  is_owned: boolean;
  citations: number;
  citation_rate: number;
  with_brand_mention: number;
  brand_mention_rate: number;
}

export interface AEOCitationsReport {
  from: string;
  to: string;
  total_answers: number;
  total_citations: number;
  answers_with_citations: number;
  owned_citation_rate: number;
  by_company: AEOCitationCompanyStat[];
  top_domains: AEOCitationDomainStat[];
}

export interface SaveAEOProfileData {
  brand_name: string;
  description: string;
  brand_aliases: string[];
  owned_domains: string[];
  competitors: AEOCompetitor[];
}

export interface ListAEOPromptsParams {
  days?: number;
  active_only?: boolean;
  offset?: number;
  limit?: number;
  sort_by?: string;
  sort_order?: 'asc' | 'desc';
}

export const aeoApi = {
  // The profile endpoint answers 404 until a brand profile has been saved. That
  // is the expected first-run state, not an error, so it collapses to null and
  // every other failure is rethrown for the caller to surface.
  getProfile: async (): Promise<AEOProfile | null> => {
    try {
      const response = await api.get<AEOProfile>('/aeo/profile');
      return response.data;
    } catch (error) {
      if (error instanceof AxiosError && error.response?.status === 404) {
        return null;
      }
      throw error;
    }
  },

  saveProfile: async (data: SaveAEOProfileData): Promise<AEOProfile> => {
    const response = await api.put<AEOProfile>('/aeo/profile', data);
    return response.data;
  },

  getPrompts: async (params?: ListAEOPromptsParams): Promise<AEOPrompt[]> => {
    const response = await api.get<AEOPrompt[]>('/aeo/prompts', { params });
    return Array.isArray(response.data) ? response.data : [];
  },

  // All-or-nothing on the API: a duplicate anywhere in the batch is a 409 and
  // nothing is saved.
  createPrompts: async (prompts: string[]): Promise<AEOPrompt[]> => {
    const response = await api.post<AEOPrompt[]>('/aeo/prompts', { prompts });
    return Array.isArray(response.data) ? response.data : [];
  },

  updatePrompt: async (
    id: number,
    data: { text?: string; is_active?: boolean }
  ): Promise<AEOPrompt> => {
    const response = await api.put<AEOPrompt>(`/aeo/prompts/${id}`, data);
    return response.data;
  },

  deletePrompt: async (id: number): Promise<void> => {
    await api.delete(`/aeo/prompts/${id}`);
  },

  // Suggestions only — nothing is persisted until the selected texts are POSTed
  // back through createPrompts.
  generatePrompts: async (count?: number): Promise<string[]> => {
    const response = await api.post<{ prompts: string[] }>('/aeo/prompts/generate', { count });
    return Array.isArray(response.data?.prompts) ? response.data.prompts : [];
  },

  getPromptAnswers: async (
    id: number,
    params?: { run_id?: number; offset?: number; limit?: number }
  ): Promise<AEOAnswer[]> => {
    const response = await api.get<AEOAnswer[]>(`/aeo/prompts/${id}/answers`, { params });
    return Array.isArray(response.data) ? response.data : [];
  },

  // 202 with the run row in `running` state; 409 when one is already in flight.
  createRun: async (): Promise<AEORun> => {
    const response = await api.post<AEORun>('/aeo/runs');
    return response.data;
  },

  // Runs one prompt (active or not) against every configured engine, under the
  // same single-run-in-flight guard as a full run.
  runPrompt: async (id: number): Promise<AEORun> => {
    const response = await api.post<AEORun>(`/aeo/prompts/${id}/run`);
    return response.data;
  },

  getRuns: async (params?: { offset?: number; limit?: number }): Promise<AEORun[]> => {
    const response = await api.get<AEORun[]>('/aeo/runs', { params });
    return Array.isArray(response.data) ? response.data : [];
  },

  getRun: async (id: number): Promise<AEORun> => {
    const response = await api.get<AEORun>(`/aeo/runs/${id}`);
    return response.data;
  },

  getDashboard: async (days?: number): Promise<AEODashboard> => {
    const response = await api.get<AEODashboard>('/aeo/dashboard', { params: { days } });
    return response.data;
  },

  getCitations: async (days?: number): Promise<AEOCitationsReport> => {
    const response = await api.get<AEOCitationsReport>('/aeo/citations', { params: { days } });
    return response.data;
  },

  getProviders: async (): Promise<AEOProviderStatus[]> => {
    const response = await api.get<AEOProviderStatus[]>('/aeo/providers');
    return Array.isArray(response.data) ? response.data : [];
  },
};
