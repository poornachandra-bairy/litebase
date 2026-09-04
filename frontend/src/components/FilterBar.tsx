import { useState } from "react";
import type { ReactNode } from "react";
import type { Column, Filter, FilterOp } from "../lib/types";

const OPERATORS: { value: FilterOp; label: string; operands: 0 | 1 | 2 | "list" }[] = [
  { value: "eq", label: "equals", operands: 1 },
  { value: "neq", label: "not equals", operands: 1 },
  { value: "gt", label: ">", operands: 1 },
  { value: "gte", label: "≥", operands: 1 },
  { value: "lt", label: "<", operands: 1 },
  { value: "lte", label: "≤", operands: 1 },
  { value: "contains", label: "contains", operands: 1 },
  { value: "starts_with", label: "starts with", operands: 1 },
  { value: "ends_with", label: "ends with", operands: 1 },
  { value: "like", label: "LIKE", operands: 1 },
  { value: "in", label: "in list", operands: "list" },
  { value: "not_in", label: "not in list", operands: "list" },
  { value: "between", label: "between", operands: 2 },
  { value: "is_null", label: "is null", operands: 0 },
  { value: "is_not_null", label: "is not null", operands: 0 },
];

function operandCount(op: FilterOp) {
  return OPERATORS.find((o) => o.value === op)?.operands ?? 1;
}

interface Props {
  columns: Column[];
  filters: Filter[];
  search: string;
  onFiltersChange: (f: Filter[]) => void;
  onSearchChange: (s: string) => void;
  onRefresh: () => void;
  right?: ReactNode;
}

/** The row browser's search box and structured filter builder. */
export function FilterBar({
  columns, filters, search, onFiltersChange, onSearchChange, onRefresh, right,
}: Props) {
  const [showFilters, setShowFilters] = useState(filters.length > 0);
  const [draftSearch, setDraftSearch] = useState(search);

  function addFilter() {
    const first = columns[0];
    if (!first) return;
    onFiltersChange([...filters, { column: first.name, op: "eq", value: "" }]);
    setShowFilters(true);
  }

  function update(i: number, patch: Partial<Filter>) {
    onFiltersChange(filters.map((f, idx) => (idx === i ? { ...f, ...patch } : f)));
  }

  return (
    <>
      <div className="toolbar">
        <input
          className="input"
          style={{ maxWidth: 260 }}
          placeholder="Search all columns…"
          value={draftSearch}
          onChange={(e) => setDraftSearch(e.target.value)}
          onKeyDown={(e) => {
            // Searching on every keystroke would query the database far too
            // often, so it is submitted explicitly.
            if (e.key === "Enter") onSearchChange(draftSearch);
            if (e.key === "Escape") { setDraftSearch(""); onSearchChange(""); }
          }}
          onBlur={() => { if (draftSearch !== search) onSearchChange(draftSearch); }}
        />
        <button className="btn sm" onClick={() => setShowFilters((v) => !v)}>
          Filters{filters.length > 0 ? ` (${filters.length})` : ""}
        </button>
        {(filters.length > 0 || search) && (
          <button className="btn ghost sm" onClick={() => {
            onFiltersChange([]);
            setDraftSearch("");
            onSearchChange("");
          }}>Clear</button>
        )}
        <button className="btn ghost sm" onClick={onRefresh} title="Refresh">↻</button>
        <span className="spacer" />
        {right}
      </div>

      {showFilters && (
        <div className="toolbar" style={{ display: "block" }}>
          {filters.map((f, i) => {
            const count = operandCount(f.op);
            return (
              <div className="filter-row" key={i}>
                <select className="select" value={f.column}
                  onChange={(e) => update(i, { column: e.target.value })}>
                  {columns.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
                </select>

                <select className="select" value={f.op}
                  onChange={(e) => {
                    const op = e.target.value as FilterOp;
                    // Switching operator type resets the operands so a stale
                    // value cannot be sent in the wrong shape.
                    update(i, { op, value: "", values: [] });
                  }}>
                  {OPERATORS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </select>

                {count === 0 ? (
                  <span className="tiny faint">no value needed</span>
                ) : count === 2 ? (
                  <div className="row-flex">
                    <input className="input" placeholder="from"
                      value={String(f.values?.[0] ?? "")}
                      onChange={(e) => update(i, { values: [e.target.value, f.values?.[1] ?? ""] })} />
                    <input className="input" placeholder="to"
                      value={String(f.values?.[1] ?? "")}
                      onChange={(e) => update(i, { values: [f.values?.[0] ?? "", e.target.value] })} />
                  </div>
                ) : count === "list" ? (
                  <input className="input" placeholder="comma,separated,values"
                    value={(f.values ?? []).join(",")}
                    onChange={(e) => update(i, {
                      values: e.target.value.split(",").map((s) => s.trim()).filter(Boolean),
                    })} />
                ) : (
                  <input className="input" placeholder="value"
                    value={String(f.value ?? "")}
                    onChange={(e) => update(i, { value: e.target.value })} />
                )}

                <button className="btn ghost sm"
                  onClick={() => onFiltersChange(filters.filter((_, idx) => idx !== i))}>×</button>
              </div>
            );
          })}
          <button className="btn sm" onClick={addFilter}>Add filter</button>
          <span className="tiny faint" style={{ marginLeft: 10 }}>
            Filters are combined with AND. Values are always sent as bound parameters.
          </span>
        </div>
      )}
    </>
  );
}
