import { api, apiClient } from '../client';
import type { CurrentUser, LoginRequest, LoginResponse, RegisterRequest } from '@/types';

export interface RegistrationStatus {
  enabled: boolean;
  // The effective single sign-on switch; the login page offers the single
  // sign-on button only when this is true. Absent means off.
  sso_enabled?: boolean;
}

export interface OIDCStartResponse {
  authorization_url: string;
}

// POST /auth/oidc/callback: the session, plus the ID token of the sign-in,
// kept with the session for the logout's id_token_hint.
export interface OIDCCallbackResponse extends LoginResponse {
  id_token: string;
}

// POST /auth/logout. end_session_url is present only for a single sign-on
// session whose ID token was sent as id_token_hint: the identity provider's
// sign-out, where the browser goes next.
export interface LogoutResponse {
  message: string;
  end_session_url?: string;
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

  // Starts an OpenID Connect sign-in: the answer is the issuer URL the
  // browser goes to. The backend refuses it while single sign-on is off.
  // The answer also sets an HttpOnly sign-in cookie that the callback must
  // send back, so a sign-in finishes only in the browser that started it.
  // Credentials are sent on this call and the callback so the browser keeps
  // and returns that cookie when the SPA and the API run on different ports
  // of the same site (dev, e2e); behind the shipped nginx they share one
  // origin and nothing changes. No other call sends credentials.
  startOIDC: async (): Promise<OIDCStartResponse> => {
    const response = await api.post<OIDCStartResponse>('/auth/oidc/start', undefined, {
      withCredentials: true,
    });
    return response.data;
  },

  // Exchanges the single-use code and state from the issuer's redirect for a
  // session, with the sign-in cookie of startOIDC. The path stays under
  // /auth/ so the client never tries a token refresh on its 401.
  oidcCallback: async (code: string, state: string): Promise<OIDCCallbackResponse> => {
    const response = await api.post<OIDCCallbackResponse>(
      '/auth/oidc/callback',
      { code, state },
      { withCredentials: true }
    );
    return response.data;
  },

  register: async (data: RegisterRequest): Promise<LoginResponse> => {
    const response = await api.post<LoginResponse>('/auth/register', data);
    return response.data;
  },

  // Revokes every refresh session of the user. The access token is passed
  // explicitly because the session's stored tokens are already gone by the
  // time the request goes out (see ApiClient.endSession). A single sign-on
  // session sends its ID token as {id_token_hint} and gets back the identity
  // provider's end-session URL, when the server has one; every other session
  // sends no body, as before. Returns that URL, or undefined.
  logout: async (accessToken?: string, idTokenHint?: string): Promise<string | undefined> => {
    const response = await api.post<LogoutResponse>(
      '/auth/logout',
      idTokenHint ? { id_token_hint: idTokenHint } : undefined,
      accessToken ? { headers: { Authorization: `Bearer ${accessToken}` } } : undefined
    );
    const endSessionURL = response?.data?.end_session_url;
    return typeof endSessionURL === 'string' && endSessionURL !== '' ? endSessionURL : undefined;
  },

  getCurrentUser: async (): Promise<CurrentUser> => {
    const response = await api.get<CurrentUser>('/users/me');
    return response.data;
  },

  requestPasswordReset: async (email: string): Promise<void> => {
    await api.post('/auth/password-reset', { email });
  },

  resetPassword: async (token: string, newPassword: string): Promise<void> => {
    await api.post('/auth/password-reset/confirm', { token, new_password: newPassword });
  },

  // Sends the current refresh token so the server renews this session, and
  // stores the replacement pair; see ApiClient.changePassword.
  changePassword: async (currentPassword: string, newPassword: string): Promise<{ renewed: boolean }> => {
    return apiClient.changePassword(currentPassword, newPassword);
  },
};