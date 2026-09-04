import { useState } from "react";
import { api } from "../lib/api";
import { useToast } from "./Toast";
import { Modal } from "./Modal";
import { Checkbox, Field } from "./common";

// The SQLite types offered in the column editor. The server validates the same
// set, so anything chosen here is guaranteed to be accepted.
const TYPES = [
  "INTEGER", "TEXT", "REAL", "BLOB", "NUMERIC", "BOOLEAN",
  "DATE", "DATETIME", "TIMESTAMP", "JSON",
];

interface DraftColumn {
  name: string;
  type: string;
  not_null: boolean;
  primary_key: boolean;
  auto_increment: boolean;
  unique: boolean;
  default: string;
}

function blankColumn(): DraftColumn {
  return {
    name: "", type: "TEXT", not_null: false,
    primary_key: false, auto_increment: false, unique: false, default: "",
  };
}

export function CreateTableModal({
  db, onClose, onCreated,
}: { db: string; onClose: () => void; onCreated: (name: string) => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [columns, setColumns] = useState<DraftColumn[]>([
    // A surrogate integer primary key is the sensible default for a new table.
    { ...blankColumn(), name: "id", type: "INTEGER", primary_key: true, auto_increment: true },
    { ...blankColumn(), name: "", type: "TEXT" },
  ]);
  const [strict, setStrict] = useState(false);
  const [busy, setBusy] = useState(false);

  function update(i: number, patch: Partial<DraftColumn>) {
    setColumns((cur) => cur.map((c, idx) => (idx === i ? { ...c, ...patch } : c)));
  }

  async function submit() {
    const usable = columns.filter((c) => c.name.trim() !== "");
    if (!name.trim() || usable.length === 0) return;

    setBusy(true);
    try {
      await api.createTable(db, {
        name: name.trim(),
        strict,
        columns: usable.map((c) => ({
          name: c.name.trim(),
          type: c.type,
          not_null: c.not_null,
          primary_key: c.primary_key,
          auto_increment: c.auto_increment,
          unique: c.unique,
          // An empty default means "no DEFAULT clause", which the API expresses
          // as null rather than an empty string.
          default: c.default.trim() === "" ? null : c.default.trim(),
        })),
      });
      toast.success(`Created table ${name}`);
      onCreated(name.trim());
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  const valid = name.trim() !== "" && columns.some((c) => c.name.trim() !== "");

  return (
    <Modal
      title="New table"
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void submit()} disabled={busy || !valid}>
            {busy ? <span className="spinner" /> : "Create table"}
          </button>
        </>
      }
    >
      <Field label="Table name" hint="Letters, digits and underscores; must not start with a digit.">
        <input className="input mono" value={name} autoFocus placeholder="products"
          onChange={(e) => setName(e.target.value)} />
      </Field>

      <div className="field">
        <label>Columns</label>
        <div className="scroll-x">
          <table className="data" style={{ minWidth: 720 }}>
            <thead>
              <tr>
                <th style={{ minWidth: 140 }}>Name</th>
                <th style={{ minWidth: 110 }}>Type</th>
                <th>PK</th>
                <th>Auto</th>
                <th>Not null</th>
                <th>Unique</th>
                <th style={{ minWidth: 110 }}>Default</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {columns.map((c, i) => (
                <tr key={i}>
                  <td>
                    <input className="input mono" value={c.name} placeholder="column_name"
                      onChange={(e) => update(i, { name: e.target.value })} />
                  </td>
                  <td>
                    <select className="select" value={c.type}
                      onChange={(e) => update(i, { type: e.target.value })}>
                      {TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                    </select>
                  </td>
                  <td style={{ textAlign: "center" }}>
                    <input type="checkbox" checked={c.primary_key}
                      onChange={(e) => update(i, {
                        primary_key: e.target.checked,
                        // AUTOINCREMENT only applies to an INTEGER primary key.
                        auto_increment: e.target.checked ? c.auto_increment : false,
                      })} />
                  </td>
                  <td style={{ textAlign: "center" }}>
                    <input type="checkbox" checked={c.auto_increment}
                      disabled={!c.primary_key || c.type !== "INTEGER"}
                      title="Requires an INTEGER primary key"
                      onChange={(e) => update(i, { auto_increment: e.target.checked })} />
                  </td>
                  <td style={{ textAlign: "center" }}>
                    <input type="checkbox" checked={c.not_null}
                      onChange={(e) => update(i, { not_null: e.target.checked })} />
                  </td>
                  <td style={{ textAlign: "center" }}>
                    <input type="checkbox" checked={c.unique} disabled={c.primary_key}
                      onChange={(e) => update(i, { unique: e.target.checked })} />
                  </td>
                  <td>
                    <input className="input mono" value={c.default} placeholder="'x' or 0"
                      onChange={(e) => update(i, { default: e.target.value })} />
                  </td>
                  <td>
                    <button className="btn ghost sm" disabled={columns.length === 1}
                      onClick={() => setColumns((cur) => cur.filter((_, idx) => idx !== i))}>×</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="row-flex" style={{ marginTop: 8 }}>
          <button className="btn sm" onClick={() => setColumns((c) => [...c, blankColumn()])}>
            Add column
          </button>
        </div>
        <div className="hint">
          Defaults must be a literal: a number, a quoted string such as <code>'pending'</code>, or
          one of NULL, TRUE, FALSE, CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_TIME.
        </div>
      </div>

      <Checkbox
        label={<>Use STRICT typing <span className="faint tiny">— SQLite rejects values that do not match the column type</span></>}
        checked={strict}
        onChange={setStrict}
      />
    </Modal>
  );
}
