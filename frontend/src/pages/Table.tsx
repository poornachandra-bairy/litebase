import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { ConfirmDialog } from "../components/Modal";
import { EmptyState, Loading } from "../components/common";
import { RowEditor } from "../components/RowEditor";
import { FilterBar } from "../components/FilterBar";
import { StructureTab } from "../components/Structure";
import { cellPreview, formatNumber, isBlob } from "../lib/format";
import type { Filter, Row, RowPage, Sort, TableDetail } from "../lib/types";

const PAGE_SIZE = 50;

export function TablePage() {
  const { db = "", table = "" } = useParams();
  const { can } = useAuth();
  const toast = useToast();

  const [detail, setDetail] = useState<TableDetail | null>(null);
  const [page, setPage] = useState<RowPage | null>(null);
  const [tab, setTab] = useState<"data" | "structure">("data");
  const [loading, setLoading] = useState(true);
  const [rowsLoading, setRowsLoading] = useState(false);

  const [filters, setFilters] = useState<Filter[]>([]);
  const [search, setSearch] = useState("");
  const [sorts, setSorts] = useState<Sort[]>([]);
  const [offset, setOffset] = useState(0);

  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [editing, setEditing] = useState<{ row: Row | null } | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [busy, setBusy] = useState(false);

  const canWrite = can("rows:write");
  const isView = detail?.kind === "view";

  const loadDetail = useCallback(async () => {
    try {
      setDetail(await api.getTable(db, table));
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [db, table]);

  const loadRows = useCallback(async () => {
    setRowsLoading(true);
    try {
      setPage(await api.listRows(db, table, {
        filters, sorts, search, limit: PAGE_SIZE, offset,
      }));
      setSelected(new Set());
    } catch (err) {
      toast.error(err);
    } finally {
      setRowsLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [db, table, filters, sorts, search, offset]);

  useEffect(() => {
    setLoading(true);
    setOffset(0);
    setFilters([]);
    setSearch("");
    setSorts([]);
    void loadDetail();
  }, [loadDetail]);

  useEffect(() => {
    void loadRows();
  }, [loadRows]);

  /** Builds the primary key object the API needs to address a row. */
  function keyOf(row: Row): Row {
    const key: Row = {};
    for (const col of page?.primary_key ?? []) key[col] = row[col];
    return key;
  }

  function toggleSort(column: string) {
    setSorts((cur) => {
      const existing = cur.find((s) => s.column === column);
      if (!existing) return [{ column, desc: false }];
      // Cycle ascending → descending → unsorted, which is what a click on a
      // column header is expected to do.
      if (!existing.desc) return [{ column, desc: true }];
      return [];
    });
    setOffset(0);
  }

  async function deleteSelected() {
    if (!page || selected.size === 0) return;
    setBusy(true);
    try {
      const keys = [...selected].map((i) => keyOf(page.rows[i]));
      const r = await api.deleteRows(db, table, keys);
      toast.success(`Deleted ${r.deleted} row${r.deleted === 1 ? "" : "s"}`);
      setDeleting(false);
      await Promise.all([loadRows(), loadDetail()]);
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function importFile(file: File) {
    const format = file.name.toLowerCase().endsWith(".json") ? "json" : "csv";
    setBusy(true);
    try {
      const r = await api.importRows(db, table, file, format);
      if (r.failed > 0) {
        toast.notify(`Imported ${r.inserted} rows; ${r.failed} failed. ${r.errors?.[0] ?? ""}`, "error");
      } else {
        toast.success(`Imported ${r.inserted} rows`);
      }
      await Promise.all([loadRows(), loadDetail()]);
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <Loading />;
  if (!detail) return <EmptyState title="Table not found" />;

  const hasKey = (page?.primary_key.length ?? 0) > 0;
  const columns = page?.columns ?? detail.columns.map((c) => c.name);

  return (
    <>
      <div className="topbar">
        <div className="breadcrumb">
          <Link to="/databases">Databases</Link> <span>/</span>
          <Link to={`/databases/${encodeURIComponent(db)}`}>{db}</Link> <span>/</span>
        </div>
        <h1>{table}</h1>
        {isView && <span className="badge">view</span>}
        <span className="spacer" />
        <span className="tiny faint">{formatNumber(detail.row_count)} rows</span>
        <Link className="btn sm" to={`/sql/${encodeURIComponent(db)}`}>SQL</Link>
      </div>

      <div className="tabs">
        <button className={`tab${tab === "data" ? " active" : ""}`} onClick={() => setTab("data")}>Data</button>
        <button className={`tab${tab === "structure" ? " active" : ""}`} onClick={() => setTab("structure")}>Structure</button>
      </div>

      {tab === "structure" ? (
        <div className="content">
          <StructureTab db={db} detail={detail} onChanged={() => { void loadDetail(); void loadRows(); }} />
        </div>
      ) : (
        <div className="content flush">
          <FilterBar
            columns={detail.columns}
            filters={filters}
            search={search}
            onFiltersChange={(f) => { setFilters(f); setOffset(0); }}
            onSearchChange={(s) => { setSearch(s); setOffset(0); }}
            onRefresh={() => { void loadRows(); void loadDetail(); }}
            right={
              <>
                <a className="btn sm" href={api.exportRowsUrl(db, table, "csv")}>CSV</a>
                <a className="btn sm" href={api.exportRowsUrl(db, table, "json")}>JSON</a>
                {canWrite && !isView && (
                  <>
                    <label className="btn sm" style={{ cursor: "pointer" }}>
                      Import
                      <input type="file" accept=".csv,.json" style={{ display: "none" }}
                        onChange={(e) => {
                          const f = e.target.files?.[0];
                          if (f) void importFile(f);
                          e.target.value = "";
                        }} />
                    </label>
                    <button className="btn primary sm" onClick={() => setEditing({ row: null })}>
                      New row
                    </button>
                  </>
                )}
              </>
            }
          />

          {selected.size > 0 && (
            <div className="toolbar">
              <span className="small">{selected.size} selected</span>
              <span className="spacer" />
              <button className="btn sm" onClick={() => setSelected(new Set())}>Clear</button>
              {canWrite && !isView && (
                <button className="btn danger sm" onClick={() => setDeleting(true)}>Delete selected</button>
              )}
            </div>
          )}

          <div className="table-wrap" style={{ flex: 1 }}>
            {rowsLoading && !page ? (
              <Loading />
            ) : page && page.rows.length === 0 ? (
              <EmptyState
                icon="▦"
                title={filters.length > 0 || search ? "No rows match" : "No rows yet"}
                hint={filters.length > 0 || search
                  ? "Try clearing the filters or the search term."
                  : canWrite && !isView ? "Add the first row to get started." : undefined}
                action={canWrite && !isView && filters.length === 0 && !search && (
                  <button className="btn primary sm" onClick={() => setEditing({ row: null })}>New row</button>
                )}
              />
            ) : (
              <table className="data">
                <thead>
                  <tr>
                    {canWrite && !isView && hasKey && (
                      <th style={{ width: 30 }}>
                        <input
                          type="checkbox"
                          checked={!!page && selected.size === page.rows.length && page.rows.length > 0}
                          onChange={(e) =>
                            setSelected(e.target.checked && page
                              ? new Set(page.rows.map((_, i) => i))
                              : new Set())
                          }
                        />
                      </th>
                    )}
                    {columns.map((col) => {
                      const sort = sorts.find((s) => s.column === col);
                      return (
                        <th key={col} className="sortable" onClick={() => toggleSort(col)}>
                          {col}
                          {sort && <span style={{ marginLeft: 4 }}>{sort.desc ? "↓" : "↑"}</span>}
                        </th>
                      );
                    })}
                    {canWrite && !isView && hasKey && <th />}
                  </tr>
                </thead>
                <tbody>
                  {page?.rows.map((row, i) => (
                    <tr key={i} className={selected.has(i) ? "selected" : ""}>
                      {canWrite && !isView && hasKey && (
                        <td>
                          <input
                            type="checkbox"
                            checked={selected.has(i)}
                            onChange={(e) => {
                              setSelected((cur) => {
                                const next = new Set(cur);
                                if (e.target.checked) next.add(i);
                                else next.delete(i);
                                return next;
                              });
                            }}
                          />
                        </td>
                      )}
                      {columns.map((col) => (
                        <td key={col} className="cell-value">
                          {row[col] === null || row[col] === undefined ? (
                            <span className="null">NULL</span>
                          ) : isBlob(row[col]) ? (
                            <span className="blob">{cellPreview(row[col])}</span>
                          ) : (
                            cellPreview(row[col])
                          )}
                        </td>
                      ))}
                      {canWrite && !isView && hasKey && (
                        <td className="actions">
                          <button className="btn ghost sm" onClick={() => setEditing({ row })}>Edit</button>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          {page && (
            <div className="pagination">
              <span>
                {page.total === 0
                  ? "No rows"
                  : `${page.offset + 1}–${page.offset + page.rows.length} of ${formatNumber(page.total)}`}
              </span>
              {rowsLoading && <span className="spinner" />}
              <span className="spacer" />
              <button className="btn sm" disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>Previous</button>
              <button className="btn sm" disabled={!page.has_more}
                onClick={() => setOffset(offset + PAGE_SIZE)}>Next</button>
            </div>
          )}
        </div>
      )}

      {editing && (
        <RowEditor
          db={db}
          table={table}
          columns={detail.columns}
          row={editing.row}
          primaryKey={page?.primary_key ?? []}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await Promise.all([loadRows(), loadDetail()]);
          }}
        />
      )}

      {deleting && (
        <ConfirmDialog
          title={`Delete ${selected.size} row${selected.size === 1 ? "" : "s"}?`}
          danger
          busy={busy}
          confirmLabel="Delete"
          message={<p>The selected rows will be permanently removed. This cannot be undone.</p>}
          onCancel={() => setDeleting(false)}
          onConfirm={() => void deleteSelected()}
        />
      )}
    </>
  );
}
