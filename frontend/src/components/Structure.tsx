import { useState } from "react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "./Toast";
import { Modal, ConfirmDialog } from "./Modal";
import { Checkbox, Field } from "./common";
import type { Column, TableDetail } from "../lib/types";

const TYPES = ["INTEGER", "TEXT", "REAL", "BLOB", "NUMERIC", "BOOLEAN", "DATE", "DATETIME", "TIMESTAMP", "JSON"];

/** The Structure tab: columns, indexes, foreign keys and the CREATE statement. */
export function StructureTab({
  db, detail, onChanged,
}: { db: string; detail: TableDetail; onChanged: () => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const canSchema = can("schema:write");
  const isView = detail.kind === "view";

  const [addingColumn, setAddingColumn] = useState(false);
  const [editingColumn, setEditingColumn] = useState<Column | null>(null);
  const [addingIndex, setAddingIndex] = useState(false);
  const [dropColumn, setDropColumn] = useState<string | null>(null);
  const [dropIndex, setDropIndex] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function confirmDropColumn() {
    if (!dropColumn) return;
    setBusy(true);
    try {
      await api.dropColumn(db, detail.name, dropColumn);
      toast.success(`Dropped column ${dropColumn}`);
      setDropColumn(null);
      onChanged();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function confirmDropIndex() {
    if (!dropIndex) return;
    setBusy(true);
    try {
      await api.dropIndex(db, dropIndex);
      toast.success(`Dropped index ${dropIndex}`);
      setDropIndex(null);
      onChanged();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="panel" style={{ marginBottom: 16 }}>
        <div className="panel-head">
          <h2>Columns</h2>
          <span className="spacer" />
          {canSchema && !isView && (
            <button className="btn sm" onClick={() => setAddingColumn(true)}>Add column</button>
          )}
        </div>
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th><th>Type</th><th>Constraints</th><th>Default</th>
                <th>References</th><th className="right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {detail.columns.map((c) => (
                <tr key={c.name}>
                  <td><strong className="mono">{c.name}</strong></td>
                  <td className="mono tiny">{c.type}</td>
                  <td>
                    {c.primary_key && <span className="badge blue">PRIMARY KEY</span>}{" "}
                    {c.auto_increment && <span className="badge">AUTOINCREMENT</span>}{" "}
                    {c.not_null && <span className="badge">NOT NULL</span>}{" "}
                    {c.unique && <span className="badge">UNIQUE</span>}
                  </td>
                  <td className="mono tiny faint">{c.default ?? "—"}</td>
                  <td className="tiny faint">
                    {c.references ? `${c.references.table}(${c.references.column})` : "—"}
                  </td>
                  <td className="actions">
                    {canSchema && !isView && (
                      <>
                        <button className="btn ghost sm" onClick={() => setEditingColumn(c)}>Edit</button>
                        <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                          onClick={() => setDropColumn(c.name)}>Drop</button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {!isView && (
        <div className="panel" style={{ marginBottom: 16 }}>
          <div className="panel-head">
            <h2>Indexes</h2>
            <span className="spacer" />
            {canSchema && <button className="btn sm" onClick={() => setAddingIndex(true)}>Add index</button>}
          </div>
          {detail.indexes.length === 0 ? (
            <div className="panel-body tiny faint">No indexes on this table.</div>
          ) : (
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr><th>Name</th><th>Columns</th><th>Type</th><th className="right">Actions</th></tr>
                </thead>
                <tbody>
                  {detail.indexes.map((idx) => (
                    <tr key={idx.name}>
                      <td className="mono tiny">{idx.name}</td>
                      <td className="mono tiny">{idx.columns.join(", ") || "—"}</td>
                      <td>
                        {idx.unique && <span className="badge">UNIQUE</span>}{" "}
                        {idx.origin !== "c" && (
                          <span className="badge" title="Created by a constraint, so SQLite manages it">
                            constraint
                          </span>
                        )}
                      </td>
                      <td className="actions">
                        {canSchema && idx.origin === "c" && (
                          <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                            onClick={() => setDropIndex(idx.name)}>Drop</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {detail.foreign_keys.length > 0 && (
        <div className="panel" style={{ marginBottom: 16 }}>
          <div className="panel-head"><h2>Foreign keys</h2></div>
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr><th>Columns</th><th>References</th><th>On delete</th><th>On update</th></tr>
              </thead>
              <tbody>
                {detail.foreign_keys.map((fk) => (
                  <tr key={fk.id}>
                    <td className="mono tiny">{fk.from.join(", ")}</td>
                    <td className="mono tiny">{fk.table}({fk.to.join(", ")})</td>
                    <td className="tiny">{fk.on_delete}</td>
                    <td className="tiny">{fk.on_update}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="panel">
        <div className="panel-head"><h2>Definition</h2></div>
        <div className="panel-body">
          <pre className="code-block" style={{ whiteSpace: "pre-wrap" }}>{detail.sql || "—"}</pre>
        </div>
      </div>

      {addingColumn && (
        <ColumnModal
          title="Add column"
          db={db} table={detail.name}
          onClose={() => setAddingColumn(false)}
          onSaved={() => { setAddingColumn(false); onChanged(); }}
        />
      )}

      {editingColumn && (
        <ColumnModal
          title={`Edit column ${editingColumn.name}`}
          db={db} table={detail.name} existing={editingColumn}
          onClose={() => setEditingColumn(null)}
          onSaved={() => { setEditingColumn(null); onChanged(); }}
        />
      )}

      {addingIndex && (
        <IndexModal
          db={db} table={detail.name} columns={detail.columns}
          onClose={() => setAddingIndex(false)}
          onSaved={() => { setAddingIndex(false); onChanged(); }}
        />
      )}

      {dropColumn && (
        <ConfirmDialog
          title={`Drop column ${dropColumn}?`} danger busy={busy} confirmLabel="Drop column"
          message={<p>All values in this column are permanently deleted. This cannot be undone.</p>}
          onCancel={() => setDropColumn(null)}
          onConfirm={() => void confirmDropColumn()}
        />
      )}

      {dropIndex && (
        <ConfirmDialog
          title={`Drop index ${dropIndex}?`} danger busy={busy} confirmLabel="Drop index"
          message={<p>Queries relying on this index may become slower. Table data is unaffected.</p>}
          onCancel={() => setDropIndex(null)}
          onConfirm={() => void confirmDropIndex()}
        />
      )}
    </>
  );
}

function ColumnModal({
  title, db, table, existing, onClose, onSaved,
}: {
  title: string; db: string; table: string;
  existing?: Column; onClose: () => void; onSaved: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(existing?.name ?? "");
  const [type, setType] = useState(existing?.type?.split("(")[0] ?? "TEXT");
  const [notNull, setNotNull] = useState(existing?.not_null ?? false);
  const [unique, setUnique] = useState(existing?.unique ?? false);
  const [defaultValue, setDefaultValue] = useState(existing?.default ?? "");
  const [busy, setBusy] = useState(false);

  const isEdit = !!existing;

  async function save() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const body = {
        name: name.trim(),
        type,
        not_null: notNull,
        unique,
        default: defaultValue.trim() === "" ? null : defaultValue.trim(),
      };
      if (isEdit) {
        await api.modifyColumn(db, table, existing!.name, body);
        toast.success(`Updated column ${name}`);
      } else {
        await api.addColumn(db, table, body);
        toast.success(`Added column ${name}`);
      }
      onSaved();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy || !name.trim()}>
            {busy ? <span className="spinner" /> : isEdit ? "Save changes" : "Add column"}
          </button>
        </>
      }
    >
      <Field label="Name">
        <input className="input mono" value={name} autoFocus onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Type">
        <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
          {TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
      </Field>
      <Field
        label="Default"
        hint="A literal such as 0, 'pending', NULL or CURRENT_TIMESTAMP. Leave blank for none."
      >
        <input className="input mono" value={defaultValue} placeholder="none"
          onChange={(e) => setDefaultValue(e.target.value)} />
      </Field>
      <div className="row-flex" style={{ marginBottom: 10 }}>
        <Checkbox label="NOT NULL" checked={notNull} onChange={setNotNull} />
        <Checkbox label="UNIQUE" checked={unique} onChange={setUnique} />
      </div>

      {isEdit ? (
        <p className="tiny" style={{ color: "var(--warning)" }}>
          SQLite cannot alter a column in place, so the table is rebuilt: its rows are copied into a
          new table inside a transaction, then indexes and triggers are recreated. Existing data is
          preserved, and the change is rolled back if anything fails.
        </p>
      ) : (
        <p className="tiny faint">
          A NOT NULL column added to a table that already has rows needs a default value.
          To add a unique column, add it first and then create a unique index.
        </p>
      )}
    </Modal>
  );
}

function IndexModal({
  db, table, columns, onClose, onSaved,
}: {
  db: string; table: string; columns: Column[];
  onClose: () => void; onSaved: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(`idx_${table}_`);
  const [selected, setSelected] = useState<string[]>([]);
  const [unique, setUnique] = useState(false);
  const [busy, setBusy] = useState(false);

  async function save() {
    if (!name.trim() || selected.length === 0) return;
    setBusy(true);
    try {
      await api.createIndex(db, { name: name.trim(), table, columns: selected, unique });
      toast.success(`Created index ${name}`);
      onSaved();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Add index"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()}
            disabled={busy || !name.trim() || selected.length === 0}>
            {busy ? <span className="spinner" /> : "Create index"}
          </button>
        </>
      }
    >
      <Field label="Index name">
        <input className="input mono" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Columns" hint="Order matters: put the column you filter on most first.">
        <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
          {columns.map((c) => (
            <Checkbox
              key={c.name}
              label={<span className="mono">{c.name} <span className="faint tiny">{c.type}</span></span>}
              checked={selected.includes(c.name)}
              onChange={(on) =>
                setSelected((cur) => (on ? [...cur, c.name] : cur.filter((n) => n !== c.name)))
              }
            />
          ))}
        </div>
      </Field>
      <Checkbox label="UNIQUE — reject duplicate values" checked={unique} onChange={setUnique} />
      {selected.length > 0 && (
        <pre className="code-block" style={{ marginTop: 12 }}>
{`CREATE ${unique ? "UNIQUE " : ""}INDEX "${name}"\n  ON "${table}" (${selected.map((c) => `"${c}"`).join(", ")});`}
        </pre>
      )}
    </Modal>
  );
}
