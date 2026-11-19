import { createContext } from 'react';
import type { User, Permission, LoginRequest, RegisterRequest } from '@/types';

export interface AuthContextType {
  user: User | null;
  // The effective permissions GET /users/me listed for the user; empty while
  // signed out and when the API listed none.
  permissions: Permission[];
  isLoading: boolean;
  isAuthenticated: boolean;
  login: (data: LoginRequest) => Promise<void>;
  // Completes an OpenID Connect sign-in from the callback page; the session
  // is kept in sessionStorage (no remember-me survives the redirect).
  loginWithSSO: (code: string, state: string) => Promise<void>;
  register: (data: RegisterRequest) => Promise<void>;
  logout: () => Promise<void>;
  refreshUser: () => Promise<void>;
}

export const AuthContext = createContext<AuthContextType | undefined>(undefined);
