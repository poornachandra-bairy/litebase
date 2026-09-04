import type * as T from "./types";

// A single fetch wrapper for the admin API.
//
// It centralises three things that every call needs: sending the session
// cookie, echoing the CSRF token back on state-changing requests, and turning
// the server's error envelope into a typed exception the UI can render.

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;
  readonly requestId?: string;

  constructor(status: number, code: string, message: string, fields = {}, requestId?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.requestId = requestId;
  }

  /** True when the session has expired and the user must sign in again. */
  get isAuthError(): boolean {
    return this.status === 401;
  }
}

const BASE = "/api/admin";

/** Reads the CSRF token the server set as a readable cookie. */
function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)litebase_csrf=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : "";
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** Raw returns the Response instead of parsing JSON, for file downloads. */
  raw?: boolean;
}

async function request<R>(path: string, opts: RequestOptions = {}): Promise<R> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = {};

  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  // Safe methods do not need a CSRF token, and sending one would be noise.
  if (!["GET", "HEAD", "OPTIONS"].includes(method)) {
    headers["X-CSRF-Token"] = csrfToken();
  }

  const res = await fetch(BASE + path, {
    method,
    headers,
    // Cookies carry the session; without this the request is anonymous.
    credentials: "same-origin",
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  });

  if (opts.raw) {
    if (!res.ok) throw await toError(res);
    return res as unknown as R;
  }
  if (res.status === 204) return undefined as R;
  if (!res.ok) throw await toError(res);

  const text = await res.text();
  if (!text) return undefined as R;
  return JSON.parse(text) as R;
}

async function toError(res: Response): Promise<ApiError> {
  let code = "error";
  let message = `Request failed with status ${res.status}`;
  let fields: Record<string, string> = {};
  let requestId: string | undefined;

  try {
    const body = await res.json();
    if (body?.error) {
      code = body.error.code ?? code;
      message = body.error.message ?? message;
      fields = body.error.fields ?? {};
      requestId = body.request_id;
    }
  } catch {
    // A non-JSON error body (a proxy error page, say) leaves the default
    // message in place rather than surfacing raw HTML.
  }
  return new ApiError(res.status, code, message, fields, requestId);
}

/** Builds a query string, omitting empty values. */
function qs(params: Record<string, string | number | boolean | undefined>): string {
  const parts = Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== "")
    .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
  return parts.length ? `?${parts.join("&")}` : "";
}

/** Path segments are encoded so a name with unusual characters cannot alter the URL. */
const seg = encodeURIComponent;

export const api = {
  // ---- session ----
  login: (email: string, password: string) =>
    request<T.Session>("/auth/login", { method: "POST", body: { email, password } }),
  logout: () => request<void>("/auth/logout", { method: "POST" }),
  me: () => request<T.Session>("/auth/me"),
  changePassword: (current_password: string, new_password: string) =>
    request<void>("/auth/change-password", {
      method: "POST",
      body: { current_password, new_password },
    }),

  // ---- databases ----
  listDatabases: () => request<{ databases: T.DatabaseMeta[] }>("/databases"),
  createDatabase: (name: string, description: string) =>
    request<T.DatabaseMeta>("/databases", { method: "POST", body: { name, description } }),
  getDatabase: (db: string) => request<T.DatabaseMeta>(`/databases/${seg(db)}`),
  updateDatabase: (db: string, description: string) =>
    request<T.DatabaseMeta>(`/databases/${seg(db)}`, { method: "PATCH", body: { description } }),
  renameDatabase: (db: string, name: string) =>
    request<T.DatabaseMeta>(`/databases/${seg(db)}/rename`, { method: "POST", body: { name } }),
  deleteDatabase: (db: string) =>
    request<void>(`/databases/${seg(db)}${qs({ confirm: db })}`, { method: "DELETE" }),
  databaseStats: (db: string) => request<T.Stats>(`/databases/${seg(db)}/stats`),
  integrityCheck: (db: string) =>
    request<T.IntegrityReport>(`/databases/${seg(db)}/integrity-check`, { method: "POST" }),
  vacuum: (db: string) =>
    request<{ status: string; duration_ms: number }>(`/databases/${seg(db)}/vacuum`, { method: "POST" }),

  /** Uploads a SQLite file as a new database. */
  async importDatabase(name: string, description: string, file: File): Promise<T.DatabaseMeta> {
    const form = new FormData();
    form.append("name", name);
    form.append("description", description);
    form.append("file", file);

    const res = await fetch(`${BASE}/databases/import`, {
      method: "POST",
      credentials: "same-origin",
      // The browser sets the multipart Content-Type with its own boundary.
      headers: { "X-CSRF-Token": csrfToken() },
      body: form,
    });
    if (!res.ok) throw await toError(res);
    return res.json();
  },

  exportDatabaseUrl: (db: string) => `${BASE}/databases/${seg(db)}/export`,

  // ---- schema ----
  getSchema: (db: string) => request<T.Schema>(`/databases/${seg(db)}/schema`),
  getTable: (db: string, table: string) =>
    request<T.TableDetail>(`/databases/${seg(db)}/tables/${seg(table)}`),
  createTable: (db: string, body: unknown) =>
    request<T.TableDetail>(`/databases/${seg(db)}/tables`, { method: "POST", body }),
  dropTable: (db: string, table: string) =>
    request<void>(`/databases/${seg(db)}/tables/${seg(table)}${qs({ confirm: table })}`, {
      method: "DELETE",
    }),
  renameTable: (db: string, table: string, name: string) =>
    request<void>(`/databases/${seg(db)}/tables/${seg(table)}/rename`, {
      method: "POST",
      body: { name },
    }),
  addColumn: (db: string, table: string, body: unknown) =>
    request<T.TableDetail>(`/databases/${seg(db)}/tables/${seg(table)}/columns`, {
      method: "POST",
      body,
    }),
  modifyColumn: (db: string, table: string, column: string, body: unknown) =>
    request<T.TableDetail>(
      `/databases/${seg(db)}/tables/${seg(table)}/columns/${seg(column)}`,
      { method: "PATCH", body },
    ),
  renameColumn: (db: string, table: string, column: string, name: string) =>
    request<T.TableDetail>(
      `/databases/${seg(db)}/tables/${seg(table)}/columns/${seg(column)}/rename`,
      { method: "POST", body: { name } },
    ),
  dropColumn: (db: string, table: string, column: string) =>
    request<T.TableDetail>(
      `/databases/${seg(db)}/tables/${seg(table)}/columns/${seg(column)}${qs({ confirm: column })}`,
      { method: "DELETE" },
    ),
  createIndex: (db: string, body: unknown) =>
    request<void>(`/databases/${seg(db)}/indexes`, { method: "POST", body }),
  dropIndex: (db: string, index: string) =>
    request<void>(`/databases/${seg(db)}/indexes/${seg(index)}`, { method: "DELETE" }),
  dropView: (db: string, view: string) =>
    request<void>(`/databases/${seg(db)}/views/${seg(view)}`, { method: "DELETE" }),
  dropTrigger: (db: string, trigger: string) =>
    request<void>(`/databases/${seg(db)}/triggers/${seg(trigger)}`, { method: "DELETE" }),

  // ---- rows ----
  listRows: (
    db: string,
    table: string,
    body: { filters?: T.Filter[]; sorts?: T.Sort[]; limit?: number; offset?: number; search?: string },
  ) =>
    request<T.RowPage>(`/databases/${seg(db)}/tables/${seg(table)}/rows/query`, {
      method: "POST",
      body,
    }),
  createRow: (db: string, table: string, values: T.Row) =>
    request<{ row: T.Row }>(`/databases/${seg(db)}/tables/${seg(table)}/rows`, {
      method: "POST",
      body: { values },
    }),
  updateRow: (db: string, table: string, key: T.Row, values: T.Row) =>
    request<{ row: T.Row }>(`/databases/${seg(db)}/tables/${seg(table)}/rows/update`, {
      method: "POST",
      body: { key, values },
    }),
  deleteRows: (db: string, table: string, keys: T.Row[]) =>
    request<{ deleted: number }>(`/databases/${seg(db)}/tables/${seg(table)}/rows/delete`, {
      method: "POST",
      body: { keys },
    }),
  exportRowsUrl: (db: string, table: string, format: "csv" | "json") =>
    `${BASE}/databases/${seg(db)}/tables/${seg(table)}/export${qs({ format })}`,

  async importRows(db: string, table: string, file: File, format: "csv" | "json") {
    const form = new FormData();
    form.append("file", file);
    form.append("format", format);
    const res = await fetch(`${BASE}/databases/${seg(db)}/tables/${seg(table)}/import`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrfToken() },
      body: form,
    });
    if (!res.ok) throw await toError(res);
    return res.json() as Promise<{ inserted: number; failed: number; errors?: string[] }>;
  },

  // ---- sql editor ----
  execute: (db: string, sql: string, maxRows?: number, signal?: AbortSignal) =>
    request<T.QueryResponse>(`/databases/${seg(db)}/query`, {
      method: "POST",
      body: { sql, max_rows: maxRows },
      signal,
    }),

  // ---- api builder ----
  listEndpoints: () => request<{ endpoints: T.Endpoint[] }>("/endpoints"),
  getEndpoint: (id: string) => request<T.Endpoint>(`/endpoints/${seg(id)}`),
  createEndpoint: (body: unknown) => request<T.Endpoint>("/endpoints", { method: "POST", body }),
  updateEndpoint: (id: string, body: unknown) =>
    request<T.Endpoint>(`/endpoints/${seg(id)}`, { method: "PUT", body }),
  deleteEndpoint: (id: string) => request<void>(`/endpoints/${seg(id)}`, { method: "DELETE" }),
  toggleEndpoint: (id: string, enabled: boolean) =>
    request<void>(`/endpoints/${seg(id)}/enabled`, { method: "POST", body: { enabled } }),
  testEndpoint: (body: unknown) =>
    request<T.QueryResponse>("/endpoints/test", { method: "POST", body }),
  openApi: () => request<Record<string, unknown>>("/openapi.json"),

  // ---- api keys ----
  listKeys: () => request<{ keys: T.ApiKey[] }>("/keys"),
  createKey: (body: unknown) =>
    request<{ key: T.ApiKey; secret: string; warning: string }>("/keys", { method: "POST", body }),
  updateKey: (id: string, body: unknown) => request<void>(`/keys/${seg(id)}`, { method: "PUT", body }),
  deleteKey: (id: string) => request<void>(`/keys/${seg(id)}`, { method: "DELETE" }),

  // ---- backups ----
  listBackups: (database?: string) =>
    request<{ backups: T.Backup[]; providers: string[]; encryption_available: boolean }>(
      `/backups${qs({ database })}`,
    ),
  createBackup: (database: string, provider: string, encrypt: boolean) =>
    request<T.Backup>("/backups", { method: "POST", body: { database, provider, encrypt } }),
  restoreBackup: (id: string, databaseName: string) =>
    request<{ status: string }>(`/backups/${seg(id)}/restore${qs({ confirm: databaseName })}`, {
      method: "POST",
    }),
  deleteBackup: (id: string) => request<void>(`/backups/${seg(id)}`, { method: "DELETE" }),
  downloadBackupUrl: (id: string) => `${BASE}/backups/${seg(id)}/download`,

  listSchedules: () => request<{ schedules: T.BackupSchedule[] }>("/backup-schedules"),
  createSchedule: (body: unknown) =>
    request<T.BackupSchedule>("/backup-schedules", { method: "POST", body }),
  updateSchedule: (id: string, body: unknown) =>
    request<T.BackupSchedule>(`/backup-schedules/${seg(id)}`, { method: "PUT", body }),
  deleteSchedule: (id: string) => request<void>(`/backup-schedules/${seg(id)}`, { method: "DELETE" }),
  runSchedule: (id: string) =>
    request<{ status: string }>(`/backup-schedules/${seg(id)}/run`, { method: "POST" }),

  // ---- settings and users ----
  getSettings: () => request<T.Settings>("/settings"),
  setGoogleDrive: (body: unknown) =>
    request<{ status: string }>("/settings/storage/gdrive", { method: "PUT", body }),
  testGoogleDrive: () => request<{ status: string }>("/settings/storage/gdrive/test", { method: "POST" }),
  deleteGoogleDrive: () => request<void>("/settings/storage/gdrive", { method: "DELETE" }),

  listUsers: () => request<{ users: T.User[]; roles: string[] }>("/users"),
  createUser: (body: unknown) => request<T.User>("/users", { method: "POST", body }),
  updateUser: (id: string, body: unknown) => request<T.User>(`/users/${seg(id)}`, { method: "PUT", body }),
  deleteUser: (id: string) => request<void>(`/users/${seg(id)}`, { method: "DELETE" }),
};
