// Shapes returned by the Litebase admin API. These mirror the Go structs in
// internal/*; keeping them in one file makes a backend change easy to trace
// through the UI.

export type Role = "owner" | "admin" | "editor" | "viewer";

export interface User {
  id: string;
  email: string;
  name: string;
  role: Role;
  disabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface Session {
  user: User;
  csrf_token: string;
  expires_at: string;
  permissions?: string[];
}

export interface DatabaseMeta {
  id: string;
  name: string;
  filename: string;
  description: string;
  created_at: string;
  updated_at: string;
  size_bytes: number;
}

export interface ForeignKeyRef {
  table: string;
  column: string;
  on_delete?: string;
  on_update?: string;
}

export interface Column {
  name: string;
  type: string;
  not_null: boolean;
  primary_key: boolean;
  auto_increment: boolean;
  unique: boolean;
  default: string | null;
  position: number;
  references?: ForeignKeyRef;
}

export interface ForeignKey {
  id: number;
  table: string;
  from: string[];
  to: string[];
  on_delete: string;
  on_update: string;
}

export interface Index {
  name: string;
  table: string;
  unique: boolean;
  partial: boolean;
  columns: string[];
  origin: string;
  sql: string;
}

export interface Trigger {
  name: string;
  table: string;
  sql: string;
}

export interface View {
  name: string;
  sql: string;
}

export interface TableSummary {
  name: string;
  kind: string;
  row_count: number;
  column_count: number;
  without_rowid: boolean;
  strict: boolean;
}

export interface TableDetail {
  name: string;
  kind: string;
  columns: Column[];
  foreign_keys: ForeignKey[];
  indexes: Index[];
  triggers: Trigger[];
  row_count: number;
  without_rowid: boolean;
  strict: boolean;
  sql: string;
}

export interface Schema {
  tables: TableSummary[];
  views: View[];
  indexes: Index[];
  triggers: Trigger[];
}

export interface Stats {
  name: string;
  size_bytes: number;
  page_size: number;
  page_count: number;
  freelist_count: number;
  encoding: string;
  journal_mode: string;
  foreign_keys: boolean;
  user_version: number;
  schema_version: number;
  table_count: number;
  view_count: number;
  index_count: number;
  trigger_count: number;
  total_rows: number;
  collected_at: string;
}

export interface IntegrityReport {
  ok: boolean;
  problems: string[];
  foreign_key_errors: { table: string; rowid: number | null; parent: string; fk_index: number }[];
  duration: string;
}

// A cell can hold any SQLite storage class. Binary arrives tagged so the UI can
// render it as binary rather than as broken text.
export type BlobCell = { $type: "blob"; base64: string; size: number };
export type Cell = string | number | boolean | null | BlobCell;
export type Row = Record<string, Cell>;

export interface RowPage {
  rows: Row[];
  columns: string[];
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
  primary_key: string[];
}

export type FilterOp =
  | "eq" | "neq" | "gt" | "gte" | "lt" | "lte"
  | "like" | "not_like" | "contains" | "starts_with" | "ends_with"
  | "in" | "not_in" | "is_null" | "is_not_null" | "between";

export interface Filter {
  column: string;
  op: FilterOp;
  value?: Cell;
  values?: Cell[];
}

export interface Sort {
  column: string;
  desc: boolean;
}

export interface QueryResult {
  statement: string;
  kind: string;
  columns: string[];
  rows: Row[];
  rows_affected: number;
  last_insert_id: number;
  duration_ms: number;
  truncated: boolean;
  error?: string;
}

export interface QueryResponse {
  results: QueryResult[];
  duration_ms: number;
  read_only: boolean;
  failed: boolean;
}

export type EndpointKind = "crud" | "query";
export type ParamIn = "query" | "path" | "body";
export type ParamType = "string" | "integer" | "number" | "boolean";

export interface EndpointParam {
  name: string;
  in: ParamIn;
  type: ParamType;
  required: boolean;
  description?: string;
  default?: unknown;
  min?: number;
  max?: number;
  min_length?: number;
  max_length?: number;
  enum?: string[];
  pattern?: string;
}

export type CRUDOperation = "list" | "read" | "create" | "update" | "delete";

export interface CRUDConfig {
  operations: CRUDOperation[];
  readable_columns?: string[];
  writable_columns?: string[];
  default_limit: number;
  max_limit: number;
  allow_filters: boolean;
  allow_search: boolean;
}

export interface Endpoint {
  id: string;
  database_id: string;
  database: string;
  name: string;
  description: string;
  kind: EndpointKind;
  method: string;
  path: string;
  table: string;
  sql: string;
  params: EndpointParam[];
  config: CRUDConfig;
  enabled: boolean;
  public: boolean;
  rate_limit: number;
  max_rows: number;
  created_at: string;
  updated_at: string;
}

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  all_endpoints: boolean;
  endpoint_ids: string[];
  rate_limit: number;
  disabled: boolean;
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface Backup {
  id: string;
  database_id: string;
  database_name: string;
  schedule_id?: string;
  filename: string;
  provider: string;
  remote_id?: string;
  size_bytes: number;
  checksum: string;
  encrypted: boolean;
  trigger: string;
  status: string;
  error?: string;
  started_at: string;
  completed_at: string | null;
}

export interface BackupSchedule {
  id: string;
  database_id: string;
  database_name: string;
  name: string;
  interval_secs: number;
  provider: string;
  encrypt: boolean;
  retention_count: number;
  retention_days: number;
  enabled: boolean;
  last_run_at: string | null;
  last_status: string;
  last_error?: string;
  next_run_at: string;
  created_at: string;
  updated_at: string;
}

export interface Settings {
  version: string;
  data_dir: string;
  providers: string[];
  encryption_available: boolean;
  supported_types: string[];
  limits: {
    max_query_rows: number;
    max_body_bytes: number;
    max_upload_bytes: number;
    query_timeout: string;
  };
  google_drive: {
    configured: boolean;
    client_id: string;
    folder_id: string;
  };
}
