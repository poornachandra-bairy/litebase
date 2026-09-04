import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { Modal, ConfirmDialog } from "../components/Modal";
import { Checkbox, EmptyState, Field, Loading } from "../components/common";
import { formatDate, timeAgo } from "../lib/format";
import type { ApiKey, Endpoint } from "../lib/types";

export function ApiKeysPage() {
  const { can } = useAuth();
  const toast = useToast();

  const [keys, setKeys] = useState<ApiKey[]>([]);
  const [endpoints, setEndpoints] = useState<Endpoint[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<ApiKey | null>(null);
  const [deleting, setDeleting] = useState<ApiKey | null>(null);
  const [revealed, setRevealed] = useState<{ secret: string; name: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const canWrite = can("keys:write");

  const reload = useCallback(async () => {
    try {
      const [k, e] = await Promise.all([api.listKeys(), api.listEndpoints()]);
      setKeys(k.keys);
      setEndpoints(e.endpoints);
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  async function confirmDelete() {
    if (!deleting) return;
    setBusy(true);
    try {
      await api.deleteKey(deleting.id);
      toast.success(`Revoked ${deleting.name}`);
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
        <h1>API Keys</h1>
        <span className="spacer" />
        {canWrite && <button className="btn primary" onClick={() => setCreating(true)}>New key</button>}
      </div>

      <div className="content">
        {keys.length === 0 ? (
          <div className="panel">
            <EmptyState
              icon="⚿"
              title="No API keys yet"
              hint="A key authenticates callers of your generated endpoints."
              action={canWrite && <button className="btn primary" onClick={() => setCreating(true)}>Create a key</button>}
            />
          </div>
        ) : (
          <div className="panel">
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Name</th><th>Key</th><th>Scopes</th><th>Endpoints</th>
                    <th>Last used</th><th>Status</th><th className="right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {keys.map((k) => {
                    const expired = k.expires_at && new Date(k.expires_at) < new Date();
                    return (
                      <tr key={k.id}>
                        <td><strong>{k.name}</strong></td>
                        <td className="mono tiny faint">{k.prefix}…</td>
                        <td>{k.scopes.map((s) => <span key={s} className="badge">{s}</span>)}</td>
                        <td className="tiny faint">
                          {k.all_endpoints ? "all" : `${k.endpoint_ids?.length ?? 0} granted`}
                        </td>
                        <td className="tiny faint nowrap">{timeAgo(k.last_used_at)}</td>
                        <td>
                          {k.disabled ? <span className="badge red">disabled</span>
                            : expired ? <span className="badge amber">expired</span>
                            : <span className="badge green">active</span>}
                          {k.expires_at && !expired && (
                            <div className="tiny faint">expires {formatDate(k.expires_at)}</div>
                          )}
                        </td>
                        <td className="actions">
                          {canWrite && (
                            <>
                              <button className="btn ghost sm" onClick={() => setEditing(k)}>Edit</button>
                              <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                                onClick={() => setDeleting(k)}>Revoke</button>
                            </>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>

      {(creating || editing) && (
        <KeyModal
          existing={editing}
          endpoints={endpoints}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={async (secret, name) => {
            setCreating(false);
            setEditing(null);
            if (secret) setRevealed({ secret, name });
            await reload();
          }}
        />
      )}

      {revealed && (
        <Modal
          title="Copy your API key"
          onClose={() => setRevealed(null)}
          footer={
            <>
              <button className="btn" onClick={() => {
                void navigator.clipboard?.writeText(revealed.secret)
                  .then(() => toast.success("Copied to clipboard"))
                  .catch(() => toast.notify("Copy failed; select the text manually", "error"));
              }}>Copy</button>
              <button className="btn primary" onClick={() => setRevealed(null)}>Done</button>
            </>
          }
        >
          <p className="small">
            This is the only time <strong>{revealed.name}</strong> can be shown. Litebase stores only a
            hash of it, so it cannot be displayed again. If you lose it, revoke this key and issue a new one.
          </p>
          <div className="secret-reveal">{revealed.secret}</div>
          <p className="tiny faint" style={{ marginTop: 10 }}>
            Send it as <code>Authorization: Bearer &lt;key&gt;</code> or <code>X-API-Key: &lt;key&gt;</code>.
          </p>
        </Modal>
      )}

      {deleting && (
        <ConfirmDialog
          title={`Revoke ${deleting.name}?`}
          danger busy={busy} confirmLabel="Revoke key"
          message={<p>Any client using this key will immediately start receiving 401 responses. This cannot be undone.</p>}
          onCancel={() => setDeleting(null)}
          onConfirm={() => void confirmDelete()}
        />
      )}
    </>
  );
}

function KeyModal({
  existing, endpoints, onClose, onSaved,
}: {
  existing: ApiKey | null;
  endpoints: Endpoint[];
  onClose: () => void;
  onSaved: (secret: string | null, name: string) => void;
}) {
  const toast = useToast();
  const isNew = existing === null;

  const [name, setName] = useState(existing?.name ?? "");
  const [scopes, setScopes] = useState<string[]>(existing?.scopes ?? ["read"]);
  const [allEndpoints, setAllEndpoints] = useState(existing?.all_endpoints ?? true);
  const [endpointIds, setEndpointIds] = useState<string[]>(existing?.endpoint_ids ?? []);
  const [rateLimit, setRateLimit] = useState(existing?.rate_limit ?? 0);
  const [disabled, setDisabled] = useState(existing?.disabled ?? false);
  const [expiresAt, setExpiresAt] = useState(
    existing?.expires_at ? existing.expires_at.slice(0, 10) : "",
  );
  const [busy, setBusy] = useState(false);

  async function save() {
    if (!name.trim() || scopes.length === 0) return;
    if (!allEndpoints && endpointIds.length === 0) {
      toast.notify("Grant at least one endpoint, or allow all endpoints", "error");
      return;
    }
    setBusy(true);
    try {
      const body = {
        name: name.trim(),
        scopes,
        all_endpoints: allEndpoints,
        endpoint_ids: allEndpoints ? [] : endpointIds,
        rate_limit: Number(rateLimit) || 0,
        // A date-only value is sent as end of day UTC so the key stays valid
        // through the day the operator picked.
        expires_at: expiresAt ? new Date(`${expiresAt}T23:59:59Z`).toISOString() : null,
      };
      if (isNew) {
        // A new key is always active, so "disabled" is not part of creation.
        const r = await api.createKey(body);
        onSaved(r.secret, r.key.name);
      } else {
        await api.updateKey(existing!.id, { ...body, disabled });
        toast.success(`Updated ${name}`);
        onSaved(null, name);
      }
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={isNew ? "New API key" : `Edit ${existing!.name}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy || !name.trim()}>
            {busy ? <span className="spinner" /> : isNew ? "Create key" : "Save changes"}
          </button>
        </>
      }
    >
      <Field label="Name" hint="A label so you can tell your keys apart, e.g. 'mobile app'.">
        <input className="input" value={name} autoFocus onChange={(e) => setName(e.target.value)} />
      </Field>

      <Field label="Scopes" hint="Read covers GET; write covers POST, PUT, PATCH and DELETE.">
        <div className="row-flex">
          {["read", "write"].map((s) => (
            <Checkbox key={s} label={s} checked={scopes.includes(s)}
              onChange={(on) => setScopes((cur) => (on ? [...cur, s] : cur.filter((x) => x !== s)))} />
          ))}
        </div>
      </Field>

      <Field label="Endpoint access">
        <Checkbox
          label="Allow every endpoint, including ones created later"
          checked={allEndpoints}
          onChange={setAllEndpoints}
        />
        {!allEndpoints && (
          <div style={{ marginTop: 8, maxHeight: 190, overflowY: "auto" }}>
            {endpoints.length === 0 ? (
              <p className="tiny faint">No endpoints have been defined yet.</p>
            ) : endpoints.map((e) => (
              <Checkbox
                key={e.id}
                label={<span className="tiny">{e.name} <span className="faint mono">/api{e.path}</span></span>}
                checked={endpointIds.includes(e.id)}
                onChange={(on) =>
                  setEndpointIds((cur) => (on ? [...cur, e.id] : cur.filter((x) => x !== e.id)))
                }
              />
            ))}
          </div>
        )}
      </Field>

      <div className="grid-2">
        <Field label="Rate limit (requests per minute)" hint="0 uses the server default.">
          <input className="input" type="number" min={0} value={rateLimit}
            onChange={(e) => setRateLimit(Number(e.target.value))} />
        </Field>
        <Field label="Expires on" hint="Leave blank for no expiry.">
          <input className="input" type="date" value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)} />
        </Field>
      </div>

      {!isNew && <Checkbox label="Disabled" checked={disabled} onChange={setDisabled} />}
    </Modal>
  );
}
