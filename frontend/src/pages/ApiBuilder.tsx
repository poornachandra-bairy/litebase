import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { ConfirmDialog } from "../components/Modal";
import { EmptyState, Loading, MethodBadge } from "../components/common";
import { EndpointEditor } from "../components/EndpointEditor";
import { EndpointTester } from "../components/EndpointTester";
import type { DatabaseMeta, Endpoint } from "../lib/types";

export function ApiBuilderPage() {
  const { can } = useAuth();
  const toast = useToast();

  const [endpoints, setEndpoints] = useState<Endpoint[]>([]);
  const [databases, setDatabases] = useState<DatabaseMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<Endpoint | "new" | null>(null);
  const [testing, setTesting] = useState<Endpoint | null>(null);
  const [deleting, setDeleting] = useState<Endpoint | null>(null);
  const [busy, setBusy] = useState(false);

  const canWrite = can("api:write");

  const reload = useCallback(async () => {
    try {
      const [e, d] = await Promise.all([api.listEndpoints(), api.listDatabases()]);
      setEndpoints(e.endpoints);
      setDatabases(d.databases);
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  async function toggle(e: Endpoint) {
    try {
      await api.toggleEndpoint(e.id, !e.enabled);
      toast.success(`${e.name} ${!e.enabled ? "enabled" : "disabled"}`);
      await reload();
    } catch (err) {
      toast.error(err);
    }
  }

  async function confirmDelete() {
    if (!deleting) return;
    setBusy(true);
    try {
      await api.deleteEndpoint(deleting.id);
      toast.success(`Deleted ${deleting.name}`);
      setDeleting(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function downloadOpenApi() {
    try {
      const doc = await api.openApi();
      // The spec is generated from the live definitions, so it is always in
      // step with what the server actually serves.
      const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "litebase-openapi.json";
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      toast.error(err);
    }
  }

  if (loading) return <Loading />;

  return (
    <>
      <div className="topbar">
        <h1>API Builder</h1>
        <span className="spacer" />
        <button className="btn" onClick={() => void downloadOpenApi()}>OpenAPI spec</button>
        {canWrite && (
          <button className="btn primary" onClick={() => setEditing("new")} disabled={databases.length === 0}>
            New endpoint
          </button>
        )}
      </div>

      <div className="content">
        {databases.length === 0 ? (
          <div className="panel">
            <EmptyState icon="🗄" title="Create a database first"
              hint="API endpoints are backed by a table or a query in one of your databases." />
          </div>
        ) : endpoints.length === 0 ? (
          <div className="panel">
            <EmptyState
              icon="⇄"
              title="No endpoints yet"
              hint="Generate CRUD routes from a table, or expose a custom SQL query with bound parameters."
              action={canWrite && (
                <button className="btn primary" onClick={() => setEditing("new")}>New endpoint</button>
              )}
            />
          </div>
        ) : (
          <div className="panel">
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Name</th><th>Routes</th><th>Database</th>
                    <th>Auth</th><th>Status</th><th className="right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {endpoints.map((e) => (
                    <tr key={e.id}>
                      <td>
                        <strong>{e.name}</strong>
                        <div className="tiny faint">
                          {e.kind === "crud" ? `CRUD on ${e.table}` : "custom query"}
                        </div>
                      </td>
                      <td>
                        {e.kind === "query" ? (
                          <div className="row-flex">
                            <MethodBadge method={e.method} />
                            <code className="tiny">/api{e.path}</code>
                          </div>
                        ) : (
                          <>
                            <code className="tiny">/api{e.path}</code>
                            <div className="tiny faint">{e.config.operations.join(", ")}</div>
                          </>
                        )}
                      </td>
                      <td className="tiny faint">{e.database}</td>
                      <td>
                        {e.public
                          ? <span className="badge amber">public</span>
                          : <span className="badge">API key</span>}
                      </td>
                      <td>
                        <span className={`badge ${e.enabled ? "green" : ""}`}>
                          {e.enabled ? "enabled" : "disabled"}
                        </span>
                      </td>
                      <td className="actions">
                        <button className="btn ghost sm" onClick={() => setTesting(e)}>Test</button>
                        {canWrite && (
                          <>
                            <button className="btn ghost sm" onClick={() => void toggle(e)}>
                              {e.enabled ? "Disable" : "Enable"}
                            </button>
                            <button className="btn ghost sm" onClick={() => setEditing(e)}>Edit</button>
                            <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                              onClick={() => setDeleting(e)}>Delete</button>
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

      {editing && (
        <EndpointEditor
          endpoint={editing === "new" ? null : editing}
          databases={databases}
          onClose={() => setEditing(null)}
          onSaved={async () => { setEditing(null); await reload(); }}
        />
      )}

      {testing && <EndpointTester endpoint={testing} onClose={() => setTesting(null)} />}

      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          danger busy={busy} confirmLabel="Delete endpoint"
          message={
            <p>
              Callers using <code>/api{deleting.path}</code> will start receiving 404 responses.
              Any API keys granted this endpoint keep working for their other grants.
            </p>
          }
          onCancel={() => setDeleting(null)}
          onConfirm={() => void confirmDelete()}
        />
      )}
    </>
  );
}
