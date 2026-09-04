import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { formatBytes, timeAgo } from "../lib/format";
import { EmptyState, Loading, StatCard } from "../components/common";
import type { Backup, DatabaseMeta, Endpoint } from "../lib/types";

export function DashboardPage() {
  const [databases, setDatabases] = useState<DatabaseMeta[]>([]);
  const [endpoints, setEndpoints] = useState<Endpoint[]>([]);
  const [backups, setBackups] = useState<Backup[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // The dashboard is a summary, so a failure in one section should not blank
    // the page; each result is taken independently.
    Promise.allSettled([api.listDatabases(), api.listEndpoints(), api.listBackups()])
      .then(([db, ep, bk]) => {
        if (db.status === "fulfilled") setDatabases(db.value.databases);
        if (ep.status === "fulfilled") setEndpoints(ep.value.endpoints);
        if (bk.status === "fulfilled") setBackups(bk.value.backups);
      })
      .finally(() => setLoading(false));
  }, []);

  if (loading) return <Loading />;

  const totalSize = databases.reduce((sum, d) => sum + d.size_bytes, 0);
  const enabledEndpoints = endpoints.filter((e) => e.enabled).length;
  const lastBackup = backups.find((b) => b.status === "completed");
  const failedBackups = backups.filter((b) => b.status === "failed").length;

  return (
    <>
      <div className="topbar">
        <h1>Dashboard</h1>
      </div>

      <div className="content">
        <div className="stat-grid" style={{ marginBottom: 20 }}>
          <StatCard
            label="Databases"
            value={databases.length}
            sub={formatBytes(totalSize) + " on disk"}
          />
          <StatCard
            label="API endpoints"
            value={endpoints.length}
            sub={`${enabledEndpoints} enabled`}
          />
          <StatCard
            label="Backups"
            value={backups.length}
            sub={lastBackup ? `last ${timeAgo(lastBackup.started_at)}` : "none yet"}
          />
          <StatCard
            label="Failed backups"
            value={failedBackups}
            sub={failedBackups > 0 ? "needs attention" : "all healthy"}
          />
        </div>

        <div className="grid-2">
          <div className="panel">
            <div className="panel-head">
              <h2>Databases</h2>
              <span className="spacer" />
              <Link className="btn sm" to="/databases">Manage</Link>
            </div>
            {databases.length === 0 ? (
              <EmptyState
                icon="🗄"
                title="No databases yet"
                hint="Create one to get started."
                action={<Link className="btn primary sm" to="/databases">Create a database</Link>}
              />
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th className="right">Size</th>
                      <th className="right">Created</th>
                    </tr>
                  </thead>
                  <tbody>
                    {databases.slice(0, 8).map((db) => (
                      <tr key={db.id}>
                        <td>
                          <Link to={`/databases/${encodeURIComponent(db.name)}`}>{db.name}</Link>
                          {db.description && (
                            <div className="tiny faint truncate">{db.description}</div>
                          )}
                        </td>
                        <td className="right mono tiny">{formatBytes(db.size_bytes)}</td>
                        <td className="right tiny faint nowrap">{timeAgo(db.created_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="panel">
            <div className="panel-head">
              <h2>Recent backups</h2>
              <span className="spacer" />
              <Link className="btn sm" to="/backups">View all</Link>
            </div>
            {backups.length === 0 ? (
              <EmptyState
                icon="⭳"
                title="No backups yet"
                hint="Run one manually or set up a schedule."
                action={<Link className="btn primary sm" to="/backups">Go to backups</Link>}
              />
            ) : (
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Database</th>
                      <th>Status</th>
                      <th className="right">Size</th>
                      <th className="right">When</th>
                    </tr>
                  </thead>
                  <tbody>
                    {backups.slice(0, 8).map((b) => (
                      <tr key={b.id}>
                        <td className="truncate">{b.database_name}</td>
                        <td>
                          <span className={`badge ${b.status === "completed" ? "green" : b.status === "failed" ? "red" : ""}`}>
                            {b.status}
                          </span>
                        </td>
                        <td className="right mono tiny">{formatBytes(b.size_bytes)}</td>
                        <td className="right tiny faint nowrap">{timeAgo(b.started_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>

        {endpoints.length > 0 && (
          <div className="panel" style={{ marginTop: 16 }}>
            <div className="panel-head">
              <h2>API endpoints</h2>
              <span className="spacer" />
              <Link className="btn sm" to="/api">Manage</Link>
            </div>
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Route</th>
                    <th>Database</th>
                    <th className="right">Requests</th>
                  </tr>
                </thead>
                <tbody>
                  {endpoints.slice(0, 6).map((e) => (
                    <tr key={e.id}>
                      <td>{e.name}</td>
                      <td className="mono tiny">
                        {e.kind === "query" ? `${e.method} /api${e.path}` : `/api${e.path}`}
                      </td>
                      <td className="tiny faint">{e.database}</td>
                      <td className="right">
                        <span className={`badge ${e.enabled ? "green" : ""}`}>
                          {e.enabled ? "enabled" : "disabled"}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>
    </>
  );
}
