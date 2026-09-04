import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { ConfirmDialog } from "../components/Modal";
import { EmptyState, Loading, StatCard } from "../components/common";
import { CreateTableModal } from "../components/CreateTable";
import { formatBytes, formatDate, formatNumber } from "../lib/format";
import type { IntegrityReport, Schema, Stats } from "../lib/types";

type Tab = "tables" | "views" | "indexes" | "triggers" | "overview";

export function DatabasePage() {
  const { db = "" } = useParams();
  const { can } = useAuth();
  const toast = useToast();
  const navigate = useNavigate();

  const [schema, setSchema] = useState<Schema | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [tab, setTab] = useState<Tab>("tables");
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [integrity, setIntegrity] = useState<IntegrityReport | null>(null);
  const [dropping, setDropping] = useState<{ kind: string; name: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const canSchema = can("schema:write");

  const reload = useCallback(async () => {
    try {
      const [s, st] = await Promise.all([api.getSchema(db), api.databaseStats(db)]);
      setSchema(s);
      setStats(st);
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
    // toast is stable from its provider.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [db]);

  useEffect(() => {
    setLoading(true);
    void reload();
  }, [reload]);

  async function runIntegrityCheck() {
    setBusy(true);
    try {
      setIntegrity(await api.integrityCheck(db));
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function runVacuum() {
    setBusy(true);
    try {
      const r = await api.vacuum(db);
      toast.success(`Vacuumed in ${r.duration_ms} ms`);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function confirmDrop() {
    if (!dropping) return;
    setBusy(true);
    try {
      const { kind, name } = dropping;
      if (kind === "table") await api.dropTable(db, name);
      else if (kind === "view") await api.dropView(db, name);
      else if (kind === "index") await api.dropIndex(db, name);
      else if (kind === "trigger") await api.dropTrigger(db, name);
      toast.success(`Dropped ${kind} ${name}`);
      setDropping(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <Loading />;
  if (!schema) return <EmptyState title="Database not found" />;

  const counts = {
    tables: schema.tables.length,
    views: schema.views.length,
    indexes: schema.indexes.length,
    triggers: schema.triggers.length,
  };

  return (
    <>
      <div className="topbar">
        <div className="breadcrumb">
          <Link to="/databases">Databases</Link> <span>/</span>
        </div>
        <h1>{db}</h1>
        <span className="spacer" />
        <Link className="btn" to={`/sql/${encodeURIComponent(db)}`}>SQL Editor</Link>
        <a className="btn" href={api.exportDatabaseUrl(db)}>Export</a>
        {canSchema && <button className="btn primary" onClick={() => setCreating(true)}>New table</button>}
      </div>

      <div className="tabs">
        {(["tables", "views", "indexes", "triggers", "overview"] as Tab[]).map((t) => (
          <button key={t} className={`tab${tab === t ? " active" : ""}`} onClick={() => setTab(t)}>
            {t[0].toUpperCase() + t.slice(1)}
            {t !== "overview" && counts[t] > 0 && (
              <span className="faint" style={{ marginLeft: 6 }}>{counts[t]}</span>
            )}
          </button>
        ))}
      </div>

      <div className="content">
        {tab === "tables" && (
          schema.tables.length === 0 ? (
            <div className="panel">
              <EmptyState
                icon="▦" title="No tables yet"
                hint="Create a table to start storing data."
                action={canSchema && <button className="btn primary" onClick={() => setCreating(true)}>New table</button>}
              />
            </div>
          ) : (
            <div className="panel">
              <div className="table-wrap">
                <table className="data clickable">
                  <thead>
                    <tr>
                      <th>Table</th>
                      <th className="right">Rows</th>
                      <th className="right">Columns</th>
                      <th>Options</th>
                      <th className="right">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {schema.tables.map((t) => (
                      <tr key={t.name}
                        onClick={() => navigate(`/databases/${encodeURIComponent(db)}/tables/${encodeURIComponent(t.name)}`)}>
                        <td><strong>{t.name}</strong></td>
                        <td className="right mono tiny">{formatNumber(t.row_count)}</td>
                        <td className="right mono tiny">{t.column_count}</td>
                        <td>
                          {t.without_rowid && <span className="badge">WITHOUT ROWID</span>}{" "}
                          {t.strict && <span className="badge">STRICT</span>}
                        </td>
                        <td className="actions" onClick={(e) => e.stopPropagation()}>
                          {canSchema && (
                            <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                              onClick={() => setDropping({ kind: "table", name: t.name })}>Drop</button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )
        )}

        {tab === "views" && (
          <SchemaObjectList
            items={schema.views.map((v) => ({ name: v.name, sql: v.sql }))}
            kind="view"
            canDrop={canSchema}
            onDrop={(name) => setDropping({ kind: "view", name })}
            onOpen={(name) => navigate(`/databases/${encodeURIComponent(db)}/tables/${encodeURIComponent(name)}`)}
          />
        )}

        {tab === "indexes" && (
          <SchemaObjectList
            items={schema.indexes.map((i) => ({
              name: i.name,
              sql: i.sql,
              detail: `on ${i.table} (${i.columns.join(", ")})${i.unique ? " · unique" : ""}`,
            }))}
            kind="index"
            canDrop={canSchema}
            onDrop={(name) => setDropping({ kind: "index", name })}
          />
        )}

        {tab === "triggers" && (
          <SchemaObjectList
            items={schema.triggers.map((t) => ({ name: t.name, sql: t.sql, detail: `on ${t.table}` }))}
            kind="trigger"
            canDrop={canSchema}
            onDrop={(name) => setDropping({ kind: "trigger", name })}
          />
        )}

        {tab === "overview" && stats && (
          <>
            <div className="stat-grid" style={{ marginBottom: 18 }}>
              <StatCard label="Size" value={formatBytes(stats.size_bytes)}
                sub={`${formatNumber(stats.page_count)} pages of ${formatBytes(stats.page_size)}`} />
              <StatCard label="Rows" value={formatNumber(stats.total_rows)} sub="across all tables" />
              <StatCard label="Tables" value={stats.table_count} sub={`${stats.index_count} indexes`} />
              <StatCard label="Free pages" value={formatNumber(stats.freelist_count)}
                sub={stats.freelist_count > 0 ? "reclaimable with VACUUM" : "nothing to reclaim"} />
            </div>

            <div className="grid-2">
              <div className="panel">
                <div className="panel-head"><h2>Configuration</h2></div>
                <div className="panel-body">
                  <dl className="kv">
                    <dt>Journal mode</dt><dd>{stats.journal_mode}</dd>
                    <dt>Encoding</dt><dd>{stats.encoding}</dd>
                    <dt>Foreign keys</dt><dd>{stats.foreign_keys ? "enforced" : "off"}</dd>
                    <dt>User version</dt><dd>{stats.user_version}</dd>
                    <dt>Schema version</dt><dd>{stats.schema_version}</dd>
                    <dt>Collected</dt><dd>{formatDate(stats.collected_at)}</dd>
                  </dl>
                </div>
              </div>

              <div className="panel">
                <div className="panel-head">
                  <h2>Maintenance</h2>
                </div>
                <div className="panel-body">
                  <div className="row-flex" style={{ marginBottom: 12 }}>
                    <button className="btn" onClick={() => void runIntegrityCheck()} disabled={busy}>
                      Run integrity check
                    </button>
                    {canSchema && (
                      <button className="btn" onClick={() => void runVacuum()} disabled={busy}>
                        Vacuum
                      </button>
                    )}
                  </div>

                  {integrity && (
                    <div className={`small`} style={{
                      background: integrity.ok ? "rgba(63,185,80,.1)" : "var(--danger-soft)",
                      border: `1px solid ${integrity.ok ? "rgba(63,185,80,.3)" : "rgba(240,93,93,.3)"}`,
                      borderRadius: 4, padding: "9px 11px",
                    }}>
                      <strong style={{ color: integrity.ok ? "var(--success)" : "var(--danger)" }}>
                        {integrity.ok ? "No problems found" : "Problems found"}
                      </strong>
                      <span className="faint"> · checked in {integrity.duration}</span>
                      {integrity.problems.length > 0 && (
                        <ul className="tiny" style={{ marginTop: 6 }}>
                          {integrity.problems.slice(0, 12).map((p, i) => <li key={i}>{p}</li>)}
                        </ul>
                      )}
                      {integrity.foreign_key_errors.length > 0 && (
                        <p className="tiny" style={{ marginTop: 6 }}>
                          {integrity.foreign_key_errors.length} foreign key violation(s).
                        </p>
                      )}
                    </div>
                  )}
                  <p className="tiny faint" style={{ marginTop: 10 }}>
                    An integrity check reads every page and can take a while on a large database.
                    Vacuum rebuilds the file to reclaim free space.
                  </p>
                </div>
              </div>
            </div>
          </>
        )}
      </div>

      {creating && (
        <CreateTableModal
          db={db}
          onClose={() => setCreating(false)}
          onCreated={async (name) => {
            setCreating(false);
            await reload();
            navigate(`/databases/${encodeURIComponent(db)}/tables/${encodeURIComponent(name)}`);
          }}
        />
      )}

      {dropping && (
        <ConfirmDialog
          title={`Drop ${dropping.kind} ${dropping.name}?`}
          danger
          busy={busy}
          confirmLabel={`Drop ${dropping.kind}`}
          message={
            dropping.kind === "table"
              ? <p>This deletes the table and <strong>every row in it</strong>. This cannot be undone.</p>
              : <p>This removes the {dropping.kind}. The underlying table data is not affected.</p>
          }
          onCancel={() => setDropping(null)}
          onConfirm={() => void confirmDrop()}
        />
      )}
    </>
  );
}

function SchemaObjectList({
  items, kind, canDrop, onDrop, onOpen,
}: {
  items: { name: string; sql: string; detail?: string }[];
  kind: string;
  canDrop: boolean;
  onDrop: (name: string) => void;
  onOpen?: (name: string) => void;
}) {
  if (items.length === 0) {
    return (
      <div className="panel">
        <EmptyState icon="◇" title={`No ${kind}s`} hint={`This database has no ${kind}s.`} />
      </div>
    );
  }
  return (
    <div className="panel">
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr><th>Name</th><th>Definition</th><th className="right">Actions</th></tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.name}>
                <td>
                  {onOpen ? (
                    <a href="#" onClick={(e) => { e.preventDefault(); onOpen(it.name); }}>
                      <strong>{it.name}</strong>
                    </a>
                  ) : <strong>{it.name}</strong>}
                  {it.detail && <div className="tiny faint">{it.detail}</div>}
                </td>
                <td className="mono tiny faint" style={{ maxWidth: 520 }}>
                  <div className="truncate" title={it.sql}>{it.sql || "—"}</div>
                </td>
                <td className="actions">
                  {canDrop && (
                    <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                      onClick={() => onDrop(it.name)}>Drop</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
