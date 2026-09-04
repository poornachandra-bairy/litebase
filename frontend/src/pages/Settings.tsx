import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { Modal, ConfirmDialog } from "../components/Modal";
import { Checkbox, EmptyState, Field, Loading } from "../components/common";
import { formatBytes, formatDate } from "../lib/format";
import type { Settings, User } from "../lib/types";

export function SettingsPage() {
  const { can, user: currentUser } = useAuth();
  const toast = useToast();

  const [settings, setSettings] = useState<Settings | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [roles, setRoles] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [showDrive, setShowDrive] = useState(false);
  const [editingUser, setEditingUser] = useState<User | "new" | null>(null);
  const [deletingUser, setDeletingUser] = useState<User | null>(null);
  const [busy, setBusy] = useState(false);

  const canUsers = can("users:read");
  const canManageUsers = can("users:write");
  const canSettings = can("settings:write");

  const reload = useCallback(async () => {
    try {
      const s = await api.getSettings();
      setSettings(s);
      if (canUsers) {
        const u = await api.listUsers();
        setUsers(u.users);
        setRoles(u.roles);
      }
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canUsers]);

  useEffect(() => { void reload(); }, [reload]);

  async function testDrive() {
    setBusy(true);
    try {
      await api.testGoogleDrive();
      toast.success("Google Drive connection is working");
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function disconnectDrive() {
    setBusy(true);
    try {
      await api.deleteGoogleDrive();
      toast.success("Google Drive disconnected");
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function confirmDeleteUser() {
    if (!deletingUser) return;
    setBusy(true);
    try {
      await api.deleteUser(deletingUser.id);
      toast.success(`Deleted ${deletingUser.email}`);
      setDeletingUser(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  if (loading) return <Loading />;
  if (!settings) return <EmptyState title="Settings unavailable" />;

  return (
    <>
      <div className="topbar"><h1>Settings</h1></div>

      <div className="content">
        <div className="grid-2" style={{ marginBottom: 16 }}>
          <div className="panel">
            <div className="panel-head"><h2>Server</h2></div>
            <div className="panel-body">
              <dl className="kv">
                <dt>Version</dt><dd>{settings.version}</dd>
                <dt>Data directory</dt><dd>{settings.data_dir}</dd>
                <dt>Storage providers</dt><dd>{settings.providers.join(", ")}</dd>
                <dt>Backup encryption</dt>
                <dd>{settings.encryption_available ? "configured" : "not configured"}</dd>
                <dt>Max query rows</dt><dd>{settings.limits.max_query_rows}</dd>
                <dt>Query timeout</dt><dd>{settings.limits.query_timeout}</dd>
                <dt>Max request body</dt><dd>{formatBytes(settings.limits.max_body_bytes)}</dd>
                <dt>Max upload</dt><dd>{formatBytes(settings.limits.max_upload_bytes)}</dd>
              </dl>
              <p className="tiny faint" style={{ marginTop: 12 }}>
                These are set through environment variables or the config file and take effect at
                startup. See <code>.env.example</code> for every option.
              </p>
            </div>
          </div>

          <div className="panel">
            <div className="panel-head">
              <h2>Google Drive backups</h2>
              <span className="spacer" />
              {settings.google_drive.configured
                ? <span className="badge green">connected</span>
                : <span className="badge">not connected</span>}
            </div>
            <div className="panel-body">
              {settings.google_drive.configured ? (
                <>
                  <dl className="kv">
                    <dt>Client id</dt><dd className="truncate">{settings.google_drive.client_id}</dd>
                    <dt>Folder</dt><dd>{settings.google_drive.folder_id || "account root"}</dd>
                  </dl>
                  <div className="row-flex" style={{ marginTop: 12 }}>
                    <button className="btn sm" onClick={() => void testDrive()} disabled={busy}>
                      Test connection
                    </button>
                    {canSettings && (
                      <>
                        <button className="btn sm" onClick={() => setShowDrive(true)}>Reconnect</button>
                        <button className="btn danger sm" onClick={() => void disconnectDrive()} disabled={busy}>
                          Disconnect
                        </button>
                      </>
                    )}
                  </div>
                </>
              ) : (
                <>
                  <p className="small muted">
                    Connect a Google Drive account to store backups off this server. Backups can be
                    encrypted before they are uploaded, so Google never holds readable data.
                  </p>
                  {canSettings && (
                    <button className="btn primary sm" style={{ marginTop: 10 }}
                      onClick={() => setShowDrive(true)}>Connect Google Drive</button>
                  )}
                </>
              )}
              {!settings.encryption_available && (
                <p className="tiny" style={{ color: "var(--warning)", marginTop: 10 }}>
                  Set <code>LITEBASE_BACKUP_ENCRYPTION_KEY</code> to enable encryption before upload.
                  It also protects the stored Drive credentials.
                </p>
              )}
            </div>
          </div>
        </div>

        {canUsers && (
          <div className="panel">
            <div className="panel-head">
              <h2>Administrators</h2>
              <span className="spacer" />
              {canManageUsers && (
                <button className="btn sm" onClick={() => setEditingUser("new")}>Add user</button>
              )}
            </div>
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Email</th><th>Name</th><th>Role</th><th>Status</th>
                    <th>Created</th><th className="right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {users.map((u) => (
                    <tr key={u.id}>
                      <td>
                        <strong>{u.email}</strong>
                        {u.id === currentUser?.id && <span className="badge blue" style={{ marginLeft: 6 }}>you</span>}
                      </td>
                      <td className="muted">{u.name || "—"}</td>
                      <td><span className="badge">{u.role}</span></td>
                      <td>
                        {u.disabled
                          ? <span className="badge red">disabled</span>
                          : <span className="badge green">active</span>}
                      </td>
                      <td className="tiny faint nowrap">{formatDate(u.created_at)}</td>
                      <td className="actions">
                        {canManageUsers && (
                          <>
                            <button className="btn ghost sm" onClick={() => setEditingUser(u)}>Edit</button>
                            {u.id !== currentUser?.id && (
                              <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                                onClick={() => setDeletingUser(u)}>Delete</button>
                            )}
                          </>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="panel-body tiny faint" style={{ borderTop: "1px solid var(--border)" }}>
              Roles: <strong>owner</strong> full control including users · <strong>admin</strong> everything
              except user management · <strong>editor</strong> read and write data ·
              <strong> viewer</strong> read only. The SQL editor runs on a read-only connection for any
              role without write access, so SQLite itself refuses writes.
            </div>
          </div>
        )}
      </div>

      {showDrive && (
        <GoogleDriveModal
          onClose={() => setShowDrive(false)}
          onSaved={async () => { setShowDrive(false); await reload(); }}
        />
      )}

      {editingUser && (
        <UserModal
          user={editingUser === "new" ? null : editingUser}
          roles={roles}
          onClose={() => setEditingUser(null)}
          onSaved={async () => { setEditingUser(null); await reload(); }}
        />
      )}

      {deletingUser && (
        <ConfirmDialog
          title={`Delete ${deletingUser.email}?`} danger busy={busy} confirmLabel="Delete user"
          message={<p>This account and all its sessions are removed. This cannot be undone.</p>}
          onCancel={() => setDeletingUser(null)}
          onConfirm={() => void confirmDeleteUser()}
        />
      )}
    </>
  );
}

function GoogleDriveModal({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [refreshToken, setRefreshToken] = useState("");
  const [folderId, setFolderId] = useState("");
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    try {
      // The server verifies the credentials against Google before storing them,
      // so a mistake surfaces here rather than at the next scheduled backup.
      await api.setGoogleDrive({
        client_id: clientId.trim(),
        client_secret: clientSecret.trim(),
        refresh_token: refreshToken.trim(),
        folder_id: folderId.trim(),
      });
      toast.success("Google Drive connected");
      onSaved();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  const complete = clientId.trim() && clientSecret.trim() && refreshToken.trim();

  return (
    <Modal
      title="Connect Google Drive"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy || !complete}>
            {busy ? <span className="spinner" /> : "Verify and connect"}
          </button>
        </>
      }
    >
      <p className="small muted" style={{ marginBottom: 14 }}>
        Create an OAuth client in the Google Cloud console with the Drive API enabled, then obtain a
        refresh token for the account that should hold the backups. The credentials are encrypted
        before they are stored.
      </p>
      <Field label="Client ID">
        <input className="input mono" value={clientId} onChange={(e) => setClientId(e.target.value)} />
      </Field>
      <Field label="Client secret">
        <input className="input mono" type="password" value={clientSecret}
          onChange={(e) => setClientSecret(e.target.value)} />
      </Field>
      <Field label="Refresh token">
        <input className="input mono" type="password" value={refreshToken}
          onChange={(e) => setRefreshToken(e.target.value)} />
      </Field>
      <Field label="Folder ID (optional)" hint="Leave blank to store backups in the account's root folder.">
        <input className="input mono" value={folderId} onChange={(e) => setFolderId(e.target.value)} />
      </Field>
    </Modal>
  );
}

function UserModal({
  user, roles, onClose, onSaved,
}: { user: User | null; roles: string[]; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const isNew = user === null;

  const [email, setEmail] = useState(user?.email ?? "");
  const [name, setName] = useState(user?.name ?? "");
  const [role, setRole] = useState(user?.role ?? "viewer");
  const [password, setPassword] = useState("");
  const [disabled, setDisabled] = useState(user?.disabled ?? false);
  const [busy, setBusy] = useState(false);

  const passwordTooShort = password !== "" && password.length < 10;

  async function save() {
    setBusy(true);
    try {
      if (isNew) {
        await api.createUser({ email: email.trim(), name: name.trim(), password, role });
        toast.success(`Created ${email}`);
      } else {
        await api.updateUser(user!.id, {
          name: name.trim(), role, disabled,
          // An empty password field means "leave the password alone".
          password: password || undefined,
        });
        toast.success(`Updated ${user!.email}`);
      }
      onSaved();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  const valid = isNew
    ? email.trim() !== "" && password.length >= 10
    : !passwordTooShort;

  return (
    <Modal
      title={isNew ? "Add administrator" : `Edit ${user!.email}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy || !valid}>
            {busy ? <span className="spinner" /> : isNew ? "Create user" : "Save changes"}
          </button>
        </>
      }
    >
      <Field label="Email">
        <input className="input" type="email" value={email} disabled={!isNew}
          onChange={(e) => setEmail(e.target.value)} />
      </Field>
      <Field label="Name (optional)">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Role">
        <select className="select" value={role} onChange={(e) => setRole(e.target.value as User["role"])}>
          {(roles.length ? roles : ["owner", "admin", "editor", "viewer"]).map((r) => (
            <option key={r} value={r}>{r}</option>
          ))}
        </select>
      </Field>
      <Field
        label={isNew ? "Password" : "New password (optional)"}
        error={passwordTooShort ? "Use at least 10 characters." : undefined}
        hint={isNew ? "At least 10 characters." : "Leave blank to keep the current password. Changing it signs the user out everywhere."}
      >
        <input className="input" type="password" value={password} autoComplete="new-password"
          onChange={(e) => setPassword(e.target.value)} />
      </Field>
      {!isNew && <Checkbox label="Disabled" checked={disabled} onChange={setDisabled} />}
    </Modal>
  );
}
