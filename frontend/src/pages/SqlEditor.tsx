import { useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { EmptyState, Loading } from "../components/common";
import { cellPreview, formatDuration, formatNumber, isBlob } from "../lib/format";
import type { DatabaseMeta, QueryResponse } from "../lib/types";

// Statements the operator has run, kept per database so switching back and
// forth does not lose recent work. This is a convenience only, so browser
// storage is the right home for it.
const HISTORY_KEY = "litebase.sql.history";
const MAX_HISTORY = 25;

function loadHistory(): string[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY);
    return raw ? (JSON.parse(raw) as string[]) : [];
  } catch {
    // Private browsing or blocked storage; history is simply unavailable.
    return [];
  }
}

function saveHistory(items: string[]) {
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(items.slice(0, MAX_HISTORY)));
  } catch {
    // Ignore: losing history must never break running a query.
  }
}

export function SqlEditorPage() {
  const { db: dbParam } = useParams();
  const navigate = useNavigate();
  const { can } = useAuth();
  const toast = useToast();

  const [databases, setDatabases] = useState<DatabaseMeta[]>([]);
  const [db, setDb] = useState(dbParam ?? "");
  const [sql, setSql] = useState("");
  const [result, setResult] = useState<QueryResponse | null>(null);
  const [running, setRunning] = useState(false);
  const [loading, setLoading] = useState(true);
  const [history, setHistory] = useState<string[]>(loadHistory);
  const abortRef = useRef<AbortController | null>(null);

  const readOnly = !can("sql:write");

  useEffect(() => {
    api.listDatabases()
      .then((r) => {
        setDatabases(r.databases);
        if (!db && r.databases.length > 0) setDb(r.databases[0].name);
      })
      .catch((err) => toast.error(err))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (db && db !== dbParam) navigate(`/sql/${encodeURIComponent(db)}`, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [db]);

  async function run() {
    if (!sql.trim() || !db) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setRunning(true);
    try {
      const res = await api.execute(db, sql, undefined, controller.signal);
      setResult(res);

      // Only remember statements that ran; a syntax error is not worth keeping.
      if (!res.failed) {
        setHistory((cur) => {
          const next = [sql.trim(), ...cur.filter((h) => h !== sql.trim())];
          saveHistory(next);
          return next.slice(0, MAX_HISTORY);
        });
      }
    } catch (err) {
      if (!controller.signal.aborted) toast.error(err);
    } finally {
      setRunning(false);
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    // Ctrl/Cmd+Enter runs, which is the convention in every SQL console.
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      void run();
    }
  }

  if (loading) return <Loading />;

  if (databases.length === 0) {
    return (
      <>
        <div className="topbar"><h1>SQL Editor</h1></div>
        <div className="content">
          <div className="panel">
            <EmptyState icon="🗄" title="No databases yet"
              hint="Create a database before running SQL." />
          </div>
        </div>
      </>
    );
  }

  return (
    <div className="sql-layout">
      <div className="topbar">
        <h1>SQL Editor</h1>
        <span className="spacer" />
        {readOnly && (
          <span className="badge amber" title="Your role allows read-only SQL; writes are refused by SQLite">
            read-only
          </span>
        )}
      </div>

      <div className="sql-toolbar">
        <select className="select" style={{ maxWidth: 200 }} value={db}
          onChange={(e) => setDb(e.target.value)}>
          {databases.map((d) => <option key={d.id} value={d.name}>{d.name}</option>)}
        </select>

        <button className="btn primary" onClick={() => void run()} disabled={running || !sql.trim()}>
          {running ? <span className="spinner" /> : "Run"}
          <span className="tiny" style={{ opacity: 0.7 }}>⌘↵</span>
        </button>
        {running && (
          <button className="btn sm" onClick={() => abortRef.current?.abort()}>Cancel</button>
        )}

        <span className="spacer" />

        {history.length > 0 && (
          <select
            className="select"
            style={{ maxWidth: 260 }}
            value=""
            onChange={(e) => { if (e.target.value) setSql(e.target.value); }}
          >
            <option value="">Recent statements…</option>
            {history.map((h, i) => (
              <option key={i} value={h}>{h.replace(/\s+/g, " ").slice(0, 70)}</option>
            ))}
          </select>
        )}
      </div>

      <textarea
        className="sql-editor"
        value={sql}
        onChange={(e) => setSql(e.target.value)}
        onKeyDown={onKeyDown}
        spellCheck={false}
        placeholder={"SELECT * FROM sqlite_schema;\n\nSeparate several statements with semicolons."}
      />

      <div className="sql-results">
        {!result ? (
          <EmptyState
            icon="›_"
            title="Run a statement to see results"
            hint={readOnly
              ? "Your role allows SELECT and other read-only statements."
              : "Press ⌘↵ (Ctrl+Enter) to run. Multiple statements run in order and stop at the first error."}
          />
        ) : (
          <>
            <div className="result-head">
              <span>
                {result.results.length} statement{result.results.length === 1 ? "" : "s"} ·{" "}
                {formatDuration(result.duration_ms)}
              </span>
              {result.failed && <span className="badge red">failed</span>}
            </div>
            {result.results.map((r, i) => (
              <div className="result-block" key={i}>
                <div className="result-head">
                  <span className="badge mono">{r.kind}</span>
                  <span className="stmt" title={r.statement}>{r.statement.replace(/\s+/g, " ")}</span>
                  <span className="nowrap">{formatDuration(r.duration_ms)}</span>
                </div>

                {r.error ? (
                  <div className="result-error">{r.error}</div>
                ) : r.columns.length > 0 ? (
                  <>
                    <div className="table-wrap" style={{ maxHeight: 460 }}>
                      <table className="data">
                        <thead>
                          <tr>{r.columns.map((c) => <th key={c}>{c}</th>)}</tr>
                        </thead>
                        <tbody>
                          {r.rows.map((row, ri) => (
                            <tr key={ri}>
                              {r.columns.map((c) => (
                                <td key={c} className="cell-value">
                                  {row[c] === null || row[c] === undefined
                                    ? <span className="null">NULL</span>
                                    : isBlob(row[c])
                                      ? <span className="blob">{cellPreview(row[c])}</span>
                                      : cellPreview(row[c])}
                                </td>
                              ))}
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                    <div className="result-head">
                      <span>{formatNumber(r.rows.length)} row{r.rows.length === 1 ? "" : "s"}</span>
                      {r.truncated && (
                        <span className="badge amber">
                          truncated — showing the first {formatNumber(r.rows.length)}
                        </span>
                      )}
                    </div>
                  </>
                ) : (
                  <div className="result-head">
                    <span>
                      {formatNumber(r.rows_affected)} row{r.rows_affected === 1 ? "" : "s"} affected
                      {r.last_insert_id > 0 && ` · last insert id ${r.last_insert_id}`}
                    </span>
                  </div>
                )}
              </div>
            ))}
          </>
        )}
      </div>
    </div>
  );
}
