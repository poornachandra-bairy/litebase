import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { Modal, ConfirmDialog } from "../components/Modal";
import { EmptyState, Field, Loading } from "../components/common";
import { formatBytes, formatDate } from "../lib/format";
import type { DatabaseMeta } from "../lib/types";

export function DatabasesPage() {
  const { can } = useAuth();
  const toast = useToast();
  const navigate = useNavigate();

  const [databases, setDatabases] = useState<DatabaseMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const [renaming, setRenaming] = useState<DatabaseMeta | null>(null);
  const [deleting, setDeleting] = useState<DatabaseMeta | null>(null);
  const [busy, setBusy] = useState(false);

  const canWrite = can("database:write");

  async function reload() {
    try {
      const r = await api.listDatabases();
      setDatabases(r.databases);
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void reload();
    // reload is stable enough for a mount-only fetch; the list is refreshed
    // explicitly after each mutation instead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function onDelete() {
    if (!deleting) return;
    setBusy(true);
    try {
      await api.deleteDatabase(deleting.name);
      toast.success(`Deleted ${deleting.name}`);
      setDeleting(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <Loading />;

  return (
    <>
      <div className="topbar">
        <h1>Databases</h1>
        <span className="spacer" />
        {canWrite && (
          <>
            <button className="btn" onClick={() => setImporting(true)}>Import</button>
            <button className="btn primary" onClick={() => setCreating(true)}>New database</button>
          </>
        )}
      </div>

      <div className="content">
        {databases.length === 0 ? (
          <div className="panel">
            <EmptyState
              icon="🗄"
              title="No databases yet"
              hint="Create an empty database, or import an existing SQLite file."
              action={
                canWrite && (
                  <div className="row-flex" style={{ justifyContent: "center" }}>
                    <button className="btn primary" onClick={() => setCreating(true)}>New database</button>
                    <button className="btn" onClick={() => setImporting(true)}>Import a file</button>
                  </div>
                )
              }
            />
          </div>
        ) : (
          <div className="panel">
            <div className="table-wrap">
              <table className="data clickable">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Description</th>
                    <th className="right">Size</th>
                    <th className="right">Created</th>
                    <th className="right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {databases.map((db) => (
                    <tr key={db.id} onClick={() => navigate(`/databases/${encodeURIComponent(db.name)}`)}>
                      <td>
                        <Link to={`/databases/${encodeURIComponent(db.name)}`} onClick={(e) => e.stopPropagation()}>
                          <strong>{db.name}</strong>
                        </Link>
                      </td>
                      <td className="muted truncate">{db.description || <span className="faint">—</span>}</td>
                      <td className="right mono tiny nowrap">{formatBytes(db.size_bytes)}</td>
                      <td className="right tiny faint nowrap">{formatDate(db.created_at)}</td>
                      <td className="actions" onClick={(e) => e.stopPropagation()}>
                        <a
                          className="btn ghost sm"
                          href={api.exportDatabaseUrl(db.name)}
                          title="Download a consistent snapshot"
                        >
                          Export
                        </a>
                        {canWrite && (
                          <>
                            <button className="btn ghost sm" onClick={() => setRenaming(db)}>Rename</button>
                            <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                              onClick={() => setDeleting(db)}>Delete</button>
                          </>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>

      {creating && (
        <CreateDatabaseModal
          onClose={() => setCreating(false)}
          onCreated={async (name) => {
            setCreating(false);
            await reload();
            navigate(`/databases/${encodeURIComponent(name)}`);
          }}
        />
      )}

      {importing && (
        <ImportDatabaseModal
          onClose={() => setImporting(false)}
          onImported={async () => {
            setImporting(false);
            await reload();
          }}
        />
      )}

      {renaming && (
        <RenameDatabaseModal
          database={renaming}
          onClose={() => setRenaming(null)}
          onRenamed={async () => {
            setRenaming(null);
            await reload();
          }}
        />
      )}

      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          danger
          busy={busy}
          confirmLabel="Delete permanently"
          message={
            <>
              <p>
                This permanently deletes the database file and every table, view and row inside it.
                <strong> This cannot be undone.</strong>
              </p>
              <p className="faint">
                Any API endpoints backed by this database will stop working. Existing backups are kept.
              </p>
            </>
          }
          onCancel={() => setDeleting(null)}
          onConfirm={() => void onDelete()}
        />
      )}
    </>
  );
}

const NAME_HINT = "Letters, digits and underscores; must not start with a digit.";

function CreateDatabaseModal({ onClose, onCreated }: { onClose: () => void; onCreated: (n: string) => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      await api.createDatabase(name.trim(), description.trim());
      toast.success(`Created ${name}`);
      onCreated(name.trim());
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New database"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void submit()} disabled={busy || !name.trim()}>
            {busy ? <span className="spinner" /> : "Create"}
          </button>
        </>
      }
    >
      <Field label="Name" hint={NAME_HINT}>
        <input className="input mono" value={name} autoFocus placeholder="my_app"
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") void submit(); }} />
      </Field>
      <Field label="Description (optional)">
        <input className="input" value={description} placeholder="What this database holds"
          onChange={(e) => setDescription(e.target.value)} />
      </Field>
    </Modal>
  );
}

function ImportDatabaseModal({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  async function submit() {
    if (!file || !name.trim()) return;
    setBusy(true);
    try {
      await api.importDatabase(name.trim(), description.trim(), file);
      toast.success(`Imported ${name}`);
      onImported();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Import a SQLite database"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void submit()} disabled={busy || !file || !name.trim()}>
            {busy ? <span className="spinner" /> : "Import"}
          </button>
        </>
      }
    >
      <Field label="SQLite file" hint="The file is verified before it is accepted.">
        <input
          ref={inputRef}
          className="input"
          type="file"
          accept=".db,.sqlite,.sqlite3,application/vnd.sqlite3,application/octet-stream"
          onChange={(e) => {
            const f = e.target.files?.[0] ?? null;
            setFile(f);
            // Offer the filename as a starting point for the database name.
            if (f && !name) {
              const base = f.name.replace(/\.(db|sqlite3?|litebase)$/i, "");
              setName(base.replace(/[^A-Za-z0-9_]/g, "_").replace(/^(\d)/, "_$1"));
            }
          }}
        />
      </Field>
      <Field label="Name" hint={NAME_HINT}>
        <input className="input mono" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Description (optional)">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      {file && <p className="tiny faint">{file.name} — {formatBytes(file.size)}</p>}
    </Modal>
  );
}

function RenameDatabaseModal({
  database, onClose, onRenamed,
}: { database: DatabaseMeta; onClose: () => void; onRenamed: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(database.name);
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!name.trim() || name === database.name) return onClose();
    setBusy(true);
    try {
      await api.renameDatabase(database.name, name.trim());
      toast.success(`Renamed to ${name}`);
      onRenamed();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={`Rename ${database.name}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void submit()} disabled={busy}>
            {busy ? <span className="spinner" /> : "Rename"}
          </button>
        </>
      }
    >
      <Field label="New name" hint={NAME_HINT}>
        <input className="input mono" value={name} autoFocus onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") void submit(); }} />
      </Field>
      <p className="tiny faint">
        API endpoints refer to this database by its identifier, so they keep working after a rename.
      </p>
    </Modal>
  );
}
