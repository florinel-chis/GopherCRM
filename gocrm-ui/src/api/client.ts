import axios, { AxiosError } from 'axios';
import type { AxiosInstance, AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import type { APIError } from '@/types';

// The `meta` block of the backend envelope. Only paginated list endpoints fill
// anything beyond the request id.
export interface ApiMeta {
  request_id?: string;
  page?: number;
  per_page?: number;
  total?: number;
  total_pages?: number;
}

// The response interceptor replaces `response.data` with the envelope payload,
// which would otherwise throw the pagination totals away. They are re-attached
// to the response itself; endpoint modules that need them cast to this type.
export type ApiResponseWithMeta<T> = AxiosResponse<T> & { meta?: ApiMeta };

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL || 'http://localhost:8080/api/v1';
const API_TIMEOUT = Number(import.meta.env.VITE_API_TIMEOUT) || 30000;

export const TOKEN_KEY = 'gophercrm_token';
export const REFRESH_TOKEN_KEY = 'gophercrm_refresh_token';

class ApiClient {
  private client: AxiosInstance;
  private isRefreshing = false;
  private refreshSubscribers: Array<(token: string) => void> = [];

  constructor() {
    this.client = axios.create({
      baseURL: API_BASE_URL,
      timeout: API_TIMEOUT,
      headers: {
        'Content-Type': 'application/json',
      },
    });

    this.setupInterceptors();
  }

  private setupInterceptors() {
    // Request interceptor
    this.client.interceptors.request.use(
      (config: InternalAxiosRequestConfig) => {
        const token = this.getToken();
        if (token && config.headers) {
          config.headers.Authorization = `Bearer ${token}`;
        }
        return config;
      },
      (error) => {
        return Promise.reject(error);
      }
    );

    // Response interceptor
    this.client.interceptors.response.use(
      (response) => {
        // Unwrap the data from the backend's response format
        if (response.data && response.data.success && response.data.data !== undefined) {
          const envelope = response.data as { data: unknown; meta?: ApiMeta };
          response.data = envelope.data;
          // Keep the envelope metadata reachable: list endpoints report their
          // total row count there and nowhere else.
          (response as ApiResponseWithMeta<unknown>).meta = envelope.meta;
        }
        return response;
      },
      async (error: AxiosError<APIError>) => {
        const originalRequest = error.config as InternalAxiosRequestConfig & { _retry?: boolean };

        // Skip token refresh for auth endpoints — a 401 on login/register
        // should show the error, not trigger a redirect loop.
        const isAuthEndpoint = originalRequest.url?.includes('/auth/');
        if (error.response?.status === 401 && !originalRequest._retry && !isAuthEndpoint) {
          if (this.isRefreshing) {
            return new Promise((resolve) => {
              this.refreshSubscribers.push((token: string) => {
                if (originalRequest.headers) {
                  originalRequest.headers.Authorization = `Bearer ${token}`;
                }
                resolve(this.client(originalRequest));
              });
            });
          }

          originalRequest._retry = true;
          this.isRefreshing = true;

          try {
            const refreshToken = this.getRefreshToken();
            if (!refreshToken) {
              throw new Error('No refresh token available');
            }

            // The refresh call goes through a bare axios instance, so the
            // unwrap interceptor above does not run — peel the
            // { success, data } envelope here.
            const response = await this.refreshToken(refreshToken);
            const payload = response.data?.data ?? response.data;
            const { token, refresh_token: rotatedRefreshToken } = payload;

            // Keep the tokens in whichever storage they already lived in, so
            // a non-"remember me" session stays in sessionStorage.
            const persist = sessionStorage.getItem(REFRESH_TOKEN_KEY) === null;

            this.setToken(token, persist);
            // Refresh tokens are rotated server-side: the one just used is
            // dead, so the replacement must be stored or the next refresh
            // will fail.
            if (rotatedRefreshToken) {
              this.setRefreshToken(rotatedRefreshToken, persist);
            }
            this.refreshSubscribers.forEach((callback) => callback(token));
            this.refreshSubscribers = [];
            
            if (originalRequest.headers) {
              originalRequest.headers.Authorization = `Bearer ${token}`;
            }
            
            return this.client(originalRequest);
          } catch (refreshError) {
            this.clearTokens();
            window.location.href = '/login';
            return Promise.reject(refreshError);
          } finally {
            this.isRefreshing = false;
          }
        }

        // Unwrap error response if it's in the wrapped format
        if (error.response?.data && 'success' in error.response.data && 'error' in error.response.data) {
          error.response.data = (error.response.data as any).error;
        }
        
        return Promise.reject(error);
      }
    );
  }

  // Uses a bare axios call (not this.client) so no Authorization header from
  // the expired session is attached — /auth/refresh is a public endpoint.
  private async refreshToken(refreshToken: string) {
    return axios.post(`${API_BASE_URL}/auth/refresh`, { refresh_token: refreshToken });
  }

  // Tokens live in localStorage only when the user opted into "remember me";
  // otherwise they go to sessionStorage so the session ends with the browser tab.
  // Reads check sessionStorage first, since that is the more specific session.
  private readToken(key: string): string | null {
    return sessionStorage.getItem(key) ?? localStorage.getItem(key);
  }

  private writeToken(key: string, token: string, persist: boolean): void {
    const target = persist ? localStorage : sessionStorage;
    const other = persist ? sessionStorage : localStorage;
    other.removeItem(key);
    target.setItem(key, token);
  }

  public getToken(): string | null {
    return this.readToken(TOKEN_KEY);
  }

  public setToken(token: string, persist = true): void {
    this.writeToken(TOKEN_KEY, token, persist);
  }

  public getRefreshToken(): string | null {
    return this.readToken(REFRESH_TOKEN_KEY);
  }

  public setRefreshToken(token: string, persist = true): void {
    this.writeToken(REFRESH_TOKEN_KEY, token, persist);
  }

  public clearTokens(): void {
    for (const key of [TOKEN_KEY, REFRESH_TOKEN_KEY]) {
      localStorage.removeItem(key);
      sessionStorage.removeItem(key);
    }
  }

  public getClient(): AxiosInstance {
    return this.client;
  }
}

export const apiClient = new ApiClient();
export const api = apiClient.getClient();