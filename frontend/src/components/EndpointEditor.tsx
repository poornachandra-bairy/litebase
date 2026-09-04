import { useEffect, useState } from "react";
import { api } from "../lib/api";
import { useToast } from "./Toast";
import { Modal } from "./Modal";
import { Checkbox, Field } from "./common";
import type {
  CRUDOperation, DatabaseMeta, Endpoint, EndpointParam, ParamIn, ParamType, Schema,
} from "../lib/types";

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"];
const OPERATIONS: CRUDOperation[] = ["list", "read", "create", "update", "delete"];
const PARAM_TYPES: ParamType[] = ["string", "integer", "number", "boolean"];
const PARAM_LOCATIONS: ParamIn[] = ["query", "path", "body"];

/** Counts "?" placeholders outside string literals and comments, mirroring the
 *  server's own check so the mismatch is caught before saving. */
function countPlaceholders(sql: string): number {
  let count = 0;
  let inSingle = false, inDouble = false, inLine = false, inBlock = false;
  for (let i = 0; i < sql.length; i++) {
    const c = sql[i], n = sql[i + 1];
    if (inLine) { if (c === "\n") inLine = false; continue; }
    if (inBlock) { if (c === "*" && n === "/") { inBlock = false; i++; } continue; }
    if (inSingle) { if (c === "'") { if (n === "'") i++; else inSingle = false; } continue; }
    if (inDouble) { if (c === '"') { if (n === '"') i++; else inDouble = false; } continue; }
    if (c === "-" && n === "-") { inLine = true; i++; continue; }
    if (c === "/" && n === "*") { inBlock = true; i++; continue; }
    if (c === "'") { inSingle = true; continue; }
    if (c === '"') { inDouble = true; continue; }
    if (c === "?") count++;
  }
  return count;
}

export function EndpointEditor({
  endpoint, databases, onClose, onSaved,
}: {
  endpoint: Endpoint | null;
  databases: DatabaseMeta[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const isNew = endpoint === null;

  const [kind, setKind] = useState<"crud" | "query">(endpoint?.kind ?? "crud");
  const [databaseId, setDatabaseId] = useState(endpoint?.database_id ?? databases[0]?.id ?? "");
  const [name, setName] = useState(endpoint?.name ?? "");
  const [description, setDescription] = useState(endpoint?.description ?? "");
  const [path, setPath] = useState(endpoint?.path ?? "");
  const [method, setMethod] = useState(endpoint?.method || "GET");
  const [table, setTable] = useState(endpoint?.table ?? "");
  const [sql, setSql] = useState(endpoint?.sql ?? "");
  const [params, setParams] = useState<EndpointParam[]>(endpoint?.params ?? []);
  const [operations, setOperations] = useState<CRUDOperation[]>(
    endpoint?.config?.operations ?? [...OPERATIONS],
  );
  const [allowFilters, setAllowFilters] = useState(endpoint?.config?.allow_filters ?? true);
  const [allowSearch, setAllowSearch] = useState(endpoint?.config?.allow_search ?? true);
  const [defaultLimit, setDefaultLimit] = useState(endpoint?.config?.default_limit ?? 50);
  const [maxLimit, setMaxLimit] = useState(endpoint?.config?.max_limit ?? 200);
  const [readableColumns, setReadableColumns] = useState<string[]>(endpoint?.config?.readable_columns ?? []);
  const [writableColumns, setWritableColumns] = useState<string[]>(endpoint?.config?.writable_columns ?? []);
  const [isPublic, setIsPublic] = useState(endpoint?.public ?? false);
  const [enabled, setEnabled] = useState(endpoint?.enabled ?? true);
  const [rateLimit, setRateLimit] = useState(endpoint?.rate_limit ?? 0);
  const [maxRows, setMaxRows] = useState(endpoint?.max_rows ?? 0);

  const [schema, setSchema] = useState<Schema | null>(null);
  const [tableColumns, setTableColumns] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  const selectedDb = databases.find((d) => d.id === databaseId);

  // Load the chosen database's schema so tables and columns can be picked from
  // a list rather than typed by hand.
  useEffect(() => {
    if (!selectedDb) return;
    let cancelled = false;
    api.getSchema(selectedDb.name)
      .then((s) => { if (!cancelled) setSchema(s); })
      .catch(() => { if (!cancelled) setSchema(null); });
    return () => { cancelled = true; };
  }, [selectedDb?.name]);

  useEffect(() => {
    if (!selectedDb || !table) { setTableColumns([]); return; }
    let cancelled = false;
    api.getTable(selectedDb.name, table)
      .then((t) => { if (!cancelled) setTableColumns(t.columns.map((c) => c.name)); })
      .catch(() => { if (!cancelled) setTableColumns([]); });
    return () => { cancelled = true; };
  }, [selectedDb?.name, table]);

  const placeholders = countPlaceholders(sql);
  const placeholderMismatch = kind === "query" && sql.trim() !== "" && placeholders !== params.length;

  function updateParam(i: number, patch: Partial<EndpointParam>) {
    setParams((cur) => cur.map((p, idx) => (idx === i ? { ...p, ...patch } : p)));
  }

  async function save() {
    setBusy(true);
    try {
      const body: Record<string, unknown> = {
        database_id: databaseId,
        kind,
        name: name.trim(),
        description: description.trim(),
        path: path.trim(),
        enabled,
        public: isPublic,
        rate_limit: Number(rateLimit) || 0,
        max_rows: Number(maxRows) || 0,
      };
      if (kind === "query") {
        body.method = method;
        body.sql = sql;
        body.params = params;
      } else {
        body.table = table;
        body.config = {
          operations,
          allow_filters: allowFilters,
          allow_search: allowSearch,
          default_limit: Number(defaultLimit) || 50,
          max_limit: Number(maxLimit) || 200,
          readable_columns: readableColumns,
          writable_columns: writableColumns,
        };
      }

      if (isNew) {
        await api.createEndpoint(body);
        toast.success(`Created ${name}`);
      } else {
        await api.updateEndpoint(endpoint!.id, body);
        toast.success(`Updated ${name}`);
      }
      onSaved();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  const valid =
    name.trim() !== "" && path.trim() !== "" && databaseId !== "" &&
    (kind === "crud" ? table !== "" : sql.trim() !== "" && !placeholderMismatch);

  return (
    <Modal
      title={isNew ? "New API endpoint" : `Edit ${endpoint!.name}`}
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy || !valid}>
            {busy ? <span className="spinner" /> : isNew ? "Create endpoint" : "Save changes"}
          </button>
        </>
      }
    >
      <div className="grid-2">
        <Field label="Type">
          <select className="select" value={kind} disabled={!isNew}
            onChange={(e) => setKind(e.target.value as "crud" | "query")}>
            <option value="crud">Automatic CRUD from a table</option>
            <option value="query">Custom SQL query</option>
          </select>
        </Field>
        <Field label="Database">
          <select className="select" value={databaseId} onChange={(e) => setDatabaseId(e.target.value)}>
            {databases.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </select>
        </Field>
      </div>

      <div className="grid-2">
        <Field label="Name">
          <input className="input" value={name} placeholder="product search"
            onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Path" hint={`Served at /api${path || "/your/path"}`}>
          <input className="input mono" value={path} placeholder="/products/search"
            onChange={(e) => setPath(e.target.value)} />
        </Field>
      </div>

      <Field label="Description (optional)">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>

      {kind === "crud" ? (
        <>
          <div className="grid-2">
            <Field label="Table">
              <select className="select" value={table} onChange={(e) => setTable(e.target.value)}>
                <option value="">Select a table…</option>
                {schema?.tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
              </select>
            </Field>
            <Field label="Operations">
              <div className="row-flex wrap">
                {OPERATIONS.map((op) => (
                  <Checkbox
                    key={op}
                    label={op}
                    checked={operations.includes(op)}
                    onChange={(on) =>
                      setOperations((cur) => (on ? [...cur, op] : cur.filter((o) => o !== op)))
                    }
                  />
                ))}
              </div>
            </Field>
          </div>

          <div className="grid-2">
            <Field label="Default page size">
              <input className="input" type="number" min={1} value={defaultLimit}
                onChange={(e) => setDefaultLimit(Number(e.target.value))} />
            </Field>
            <Field label="Maximum page size">
              <input className="input" type="number" min={1} value={maxLimit}
                onChange={(e) => setMaxLimit(Number(e.target.value))} />
            </Field>
          </div>

          <div className="row-flex" style={{ marginBottom: 12 }}>
            <Checkbox label="Allow filter parameters" checked={allowFilters} onChange={setAllowFilters} />
            <Checkbox label="Allow search" checked={allowSearch} onChange={setAllowSearch} />
          </div>

          {tableColumns.length > 0 && (
            <>
              <Field label="Readable columns"
                hint="Leave all unchecked to expose every column. Use this to hide a password or token column.">
                <div className="row-flex wrap">
                  {tableColumns.map((c) => (
                    <Checkbox key={c} label={<span className="mono tiny">{c}</span>}
                      checked={readableColumns.includes(c)}
                      onChange={(on) => setReadableColumns((cur) =>
                        on ? [...cur, c] : cur.filter((x) => x !== c))} />
                  ))}
                </div>
              </Field>
              <Field label="Writable columns"
                hint="Leave all unchecked to allow writing every column. A request setting anything else is rejected.">
                <div className="row-flex wrap">
                  {tableColumns.map((c) => (
                    <Checkbox key={c} label={<span className="mono tiny">{c}</span>}
                      checked={writableColumns.includes(c)}
                      onChange={(on) => setWritableColumns((cur) =>
                        on ? [...cur, c] : cur.filter((x) => x !== c))} />
                  ))}
                </div>
              </Field>
            </>
          )}
        </>
      ) : (
        <>
          <Field label="Method">
            <select className="select" style={{ maxWidth: 160 }} value={method}
              onChange={(e) => setMethod(e.target.value)}>
              {METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </Field>

          <Field
            label="SQL"
            hint="Use ? placeholders. Values are bound as parameters and never inserted into the statement text."
            error={placeholderMismatch
              ? `The statement has ${placeholders} placeholder(s) but ${params.length} parameter(s) are declared.`
              : undefined}
          >
            <textarea className="textarea" rows={6} value={sql} spellCheck={false}
              placeholder={"SELECT * FROM products\nWHERE category = ?\n  AND price <= ?\nORDER BY created_at DESC\nLIMIT ?;"}
              onChange={(e) => setSql(e.target.value)} />
          </Field>

          <div className="field">
            <label>Parameters — bound in this order</label>
            {params.map((p, i) => (
              <div className="param-row" key={i}>
                <input className="input mono" value={p.name} placeholder="name"
                  onChange={(e) => updateParam(i, { name: e.target.value })} />
                <select className="select" value={p.in}
                  onChange={(e) => updateParam(i, { in: e.target.value as ParamIn })}>
                  {PARAM_LOCATIONS.map((l) => <option key={l} value={l}>{l}</option>)}
                </select>
                <select className="select" value={p.type}
                  onChange={(e) => updateParam(i, { type: e.target.value as ParamType })}>
                  {PARAM_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
                <Checkbox label="req" checked={p.required}
                  onChange={(on) => updateParam(i, { required: on })} />
                <button className="btn ghost sm"
                  onClick={() => setParams((cur) => cur.filter((_, idx) => idx !== i))}>×</button>
              </div>
            ))}
            <button
              className="btn sm"
              onClick={() =>
                setParams((cur) => [...cur, { name: "", in: "query", type: "string", required: false }])
              }
            >
              Add parameter
            </button>
            <div className="hint">
              {placeholders} placeholder{placeholders === 1 ? "" : "s"} in the statement ·{" "}
              {params.length} parameter{params.length === 1 ? "" : "s"} declared.
            </div>
          </div>
        </>
      )}

      <hr className="divider" />

      <div className="grid-2">
        <Field label="Rate limit (requests per minute)" hint="0 uses the server default.">
          <input className="input" type="number" min={0} value={rateLimit}
            onChange={(e) => setRateLimit(Number(e.target.value))} />
        </Field>
        <Field label="Maximum rows returned" hint="0 uses the server default.">
          <input className="input" type="number" min={0} value={maxRows}
            onChange={(e) => setMaxRows(Number(e.target.value))} />
        </Field>
      </div>

      <div className="row-flex">
        <Checkbox label="Enabled" checked={enabled} onChange={setEnabled} />
        <Checkbox
          label={<>Public <span className="faint tiny">— callable with no API key</span></>}
          checked={isPublic}
          onChange={setIsPublic}
        />
      </div>
      {isPublic && (
        <p className="tiny" style={{ color: "var(--warning)", marginTop: 8 }}>
          A public endpoint is reachable by anyone who can reach this server. Only enable it for data
          you are happy to publish.
        </p>
      )}
    </Modal>
  );
}
