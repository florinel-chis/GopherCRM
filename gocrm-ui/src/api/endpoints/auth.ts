import { api } from '../client';
import type { LoginRequest, LoginResponse, RegisterRequest, User } from '@/types';

export interface RegistrationStatus {
  enabled: boolean;
}

export const authApi = {
  // Public probe of the security.allow_public_registration switch, so the
  // login and register screens know whether to offer sign-up before any
  // session exists. The backend enforces the switch on POST /auth/register
  // regardless of what this reports.
  getRegistrationStatus: async (): Promise<RegistrationStatus> => {
    const response = await api.get<RegistrationStatus>('/auth/registration');
    return response.data;
  },

  login: async (data: LoginRequest): Promise<LoginResponse> => {
    const response = await api.post<LoginResponse>('/auth/login', data);
    return response.data;
  },

  register: async (data: RegisterRequest): Promise<LoginResponse> => {
    const response = await api.post<LoginResponse>('/auth/register', data);
    return response.data;
  },

  logout: async (): Promise<void> => {
    await api.post('/auth/logout');
  },

  getCurrentUser: async (): Promise<User> => {
    const response = await api.get<User>('/users/me');
    return response.data;
  },

  refreshToken: async (refreshToken: string): Promise<LoginResponse> => {
    const response = await api.post<LoginResponse>('/auth/refresh', { refresh_token: refreshToken });
    return response.data;
  },

  requestPasswordReset: async (email: string): Promise<void> => {
    await api.post('/auth/password-reset', { email });
  },

  resetPassword: async (token: string, newPassword: string): Promise<void> => {
    await api.post('/auth/password-reset/confirm', { token, new_password: newPassword });
  },

  changePassword: async (currentPassword: string, newPassword: string): Promise<void> => {
    await api.post('/auth/change-password', {
      current_password: currentPassword,
      new_password: newPassword,
    });
  },
};