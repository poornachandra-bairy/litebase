import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api, ApiError } from "./api";
import type { Session, User } from "./types";

interface AuthState {
  user: User | null;
  permissions: Set<string>;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  /** Checks a permission such as "sql:write" against the signed-in user. */
  can: (permission: string) => boolean;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [permissions, setPermissions] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);

  const apply = useCallback((session: Session | null) => {
    setUser(session?.user ?? null);
    setPermissions(new Set(session?.permissions ?? []));
  }, []);

  const refresh = useCallback(async () => {
    try {
      apply(await api.me());
    } catch (err) {
      // A 401 here simply means nobody is signed in yet, which is the normal
      // state on first load rather than a failure worth surfacing.
      if (!(err instanceof ApiError && err.isAuthError)) {
        console.error("session check failed", err);
      }
      apply(null);
    } finally {
      setLoading(false);
    }
  }, [apply]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(
    async (email: string, password: string) => {
      const session = await api.login(email, password);
      // The login response omits the permission list, so it is fetched to keep
      // the UI's capability checks accurate from the first render.
      try {
        apply(await api.me());
      } catch {
        apply(session);
      }
    },
    [apply],
  );

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      apply(null);
    }
  }, [apply]);

  const value = useMemo<AuthState>(
    () => ({
      user,
      permissions,
      loading,
      login,
      logout,
      refresh,
      // The server enforces permissions on every request; this only decides
      // whether to render a control, so a stale value cannot grant access.
      can: (permission: string) => permissions.has(permission),
    }),
    [user, permissions, loading, login, logout, refresh],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside an AuthProvider");
  return ctx;
}
