import { useState } from "react";
import { api } from "../lib/api";
import { useToast } from "./Toast";
import { Modal } from "./Modal";
import { cellToInput, isBlob } from "../lib/format";
import type { Cell, Column, Row } from "../lib/types";

interface Props {
  db: string;
  table: string;
  columns: Column[];
  /** null creates a new row; otherwise the row being edited. */
  row: Row | null;
  primaryKey: string[];
  onClose: () => void;
  onSaved: () => void;
}

interface FieldState {
  text: string;
  isNull: boolean;
}

/** Creates or edits a single row. */
export function RowEditor({ db, table, columns, row, primaryKey, onClose, onSaved }: Props) {
  const toast = useToast();
  const isNew = row === null;
  const [busy, setBusy] = useState(false);

  const [fields, setFields] = useState<Record<string, FieldState>>(() => {
    const initial: Record<string, FieldState> = {};
    for (const c of columns) {
      const value = row?.[c.name];
      initial[c.name] = {
        text: value === undefined ? "" : cellToInput(value as Cell),
        // A new row starts with nullable columns unset so their defaults apply.
        isNull: isNew ? !c.not_null && c.default === null : value === null || value === undefined,
      };
    }
    return initial;
  });

  function set(name: string, patch: Partial<FieldState>) {
    setFields((cur) => ({ ...cur, [name]: { ...cur[name], ...patch } }));
  }

  /** Builds the values payload, skipping fields that should keep their default. */
  function buildValues(): Row {
    const values: Row = {};
    for (const c of columns) {
      const f = fields[c.name];
      if (!f) continue;

      // On create, a generated key left blank must be omitted so SQLite
      // assigns it.
      if (isNew && c.primary_key && c.auto_increment && f.text.trim() === "") continue;
      // An untouched nullable field on create is omitted so its DEFAULT applies.
      if (isNew && f.isNull && f.text === "" && c.default !== null) continue;

      values[c.name] = f.isNull ? null : f.text;
    }
    return values;
  }

  async function save() {
    setBusy(true);
    try {
      const values = buildValues();
      if (isNew) {
        await api.createRow(db, table, values);
        toast.success("Row created");
      } else {
        const key: Row = {};
        for (const col of primaryKey) key[col] = row![col];
        await api.updateRow(db, table, key, values);
        toast.success("Row updated");
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
      title={isNew ? "New row" : "Edit row"}
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy}>
            {busy ? <span className="spinner" /> : isNew ? "Create row" : "Save changes"}
          </button>
        </>
      }
    >
      {columns.map((c) => {
        const f = fields[c.name];
        if (!f) return null;
        const generated = isNew && c.primary_key && c.auto_increment;
        const blobValue = row?.[c.name] !== undefined && isBlob(row[c.name] as Cell);

        return (
          <div className="field" key={c.name}>
            <label>
              {c.name}{" "}
              <span className="faint tiny mono">{c.type}</span>{" "}
              {c.primary_key && <span className="badge blue">PK</span>}{" "}
              {c.not_null && <span className="badge">NOT NULL</span>}{" "}
              {c.unique && <span className="badge">UNIQUE</span>}
            </label>

            <div className="row-flex">
              <input
                className="input mono"
                value={f.isNull ? "" : f.text}
                // Typing clears the NULL flag, so the field stays usable
                // without first having to untick the checkbox.
                disabled={generated}
                placeholder={
                  generated ? "assigned automatically"
                    : c.default !== null ? `default ${c.default}` : ""
                }
                onChange={(e) => set(c.name, { text: e.target.value, isNull: false })}
              />
              <label className="check nowrap" title={c.not_null ? "This column does not accept NULL" : "Store NULL"}>
                <input
                  type="checkbox"
                  checked={f.isNull}
                  disabled={c.not_null || generated}
                  onChange={(e) => set(c.name, { isNull: e.target.checked })}
                />
                <span className="tiny">NULL</span>
              </label>
            </div>

            {blobValue && (
              <div className="hint">
                This column holds binary data, shown as base64. Editing it replaces the stored bytes.
              </div>
            )}
            {c.references && (
              <div className="hint">
                References {c.references.table}({c.references.column})
              </div>
            )}
          </div>
        );
      })}

      {!isNew && primaryKey.length === 0 && (
        <p className="tiny" style={{ color: "var(--warning)" }}>
          This table has no primary key, so rows are addressed by their internal rowid.
        </p>
      )}
    </Modal>
  );
}
