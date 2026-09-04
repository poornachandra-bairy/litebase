import { useCallback, useEffect, useState } from "react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useToast } from "../components/Toast";
import { Modal, ConfirmDialog } from "../components/Modal";
import { Checkbox, EmptyState, Field, Loading } from "../components/common";
import { formatBytes, formatDate, formatInterval, timeAgo } from "../lib/format";
import type { Backup, BackupSchedule, DatabaseMeta } from "../lib/types";

export function BackupsPage() {
  const { can } = useAuth();
  const toast = useToast();

  const [backups, setBackups] = useState<Backup[]>([]);
  const [schedules, setSchedules] = useState<BackupSchedule[]>([]);
  const [databases, setDatabases] = useState<DatabaseMeta[]>([]);
  const [providers, setProviders] = useState<string[]>([]);
  const [encryptionAvailable, setEncryptionAvailable] = useState(false);
  const [tab, setTab] = useState<"history" | "schedules">("history");
  const [loading, setLoading] = useState(true);

  const [running, setRunning] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState<BackupSchedule | "new" | null>(null);
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [deleting, setDeleting] = useState<Backup | null>(null);
  const [deletingSchedule, setDeletingSchedule] = useState<BackupSchedule | null>(null);
  const [busy, setBusy] = useState(false);

  const canWrite = can("backup:write");

  const reload = useCallback(async () => {
    try {
      const [b, s, d] = await Promise.all([api.listBackups(), api.listSchedules(), api.listDatabases()]);
      setBackups(b.backups);
      setProviders(b.providers);
      setEncryptionAvailable(b.encryption_available);
      setSchedules(s.schedules);
      setDatabases(d.databases);
    } catch (err) {
      toast.error(err);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  async function confirmRestore() {
    if (!restoring) return;
    setBusy(true);
    try {
      await api.restoreBackup(restoring.id, restoring.database_name);
      toast.success(`Restored ${restoring.database_name}`);
      setRestoring(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function confirmDelete() {
    if (!deleting) return;
    setBusy(true);
    try {
      await api.deleteBackup(deleting.id);
      toast.success("Backup deleted");
      setDeleting(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function confirmDeleteSchedule() {
    if (!deletingSchedule) return;
    setBusy(true);
    try {
      await api.deleteSchedule(deletingSchedule.id);
      toast.success("Schedule deleted");
      setDeletingSchedule(null);
      await reload();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  async function runNow(s: BackupSchedule) {
    setBusy(true);
    try {
      await api.runSchedule(s.id);
      toast.success(`Ran ${s.name}`);
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
        <h1>Backups</h1>
        <span className="spacer" />
        {canWrite && (
          <>
            <button className="btn" onClick={() => setEditingSchedule("new")}
              disabled={databases.length === 0}>New schedule</button>
            <button className="btn primary" onClick={() => setRunning(true)}
              disabled={databases.length === 0}>Back up now</button>
          </>
        )}
      </div>

      <div className="tabs">
        <button className={`tab${tab === "history" ? " active" : ""}`} onClick={() => setTab("history")}>
          History{backups.length > 0 && <span className="faint" style={{ marginLeft: 6 }}>{backups.length}</span>}
        </button>
        <button className={`tab${tab === "schedules" ? " active" : ""}`} onClick={() => setTab("schedules")}>
          Schedules{schedules.length > 0 && <span className="faint" style={{ marginLeft: 6 }}>{schedules.length}</span>}
        </button>
      </div>

      <div className="content">
        {!encryptionAvailable && (
          <div className="panel" style={{ marginBottom: 14, borderColor: "rgba(210,153,34,.35)" }}>
            <div className="panel-body small" style={{ color: "var(--warning)" }}>
              Backup encryption is unavailable because no encryption key is configured.
              Set <code>LITEBASE_BACKUP_ENCRYPTION_KEY</code> to encrypt backups before they are
              uploaded to remote storage.
            </div>
          </div>
        )}

        {tab === "history" ? (
          backups.length === 0 ? (
            <div className="panel">
              <EmptyState
                icon="⭳" title="No backups yet"
                hint="Take one now, or set a schedule so they run automatically."
                action={canWrite && <button className="btn primary" onClick={() => setRunning(true)}>Back up now</button>}
              />
            </div>
          ) : (
            <div className="panel">
              <div className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      <th>Database</th><th>When</th><th>Trigger</th><th>Storage</th>
                      <th className="right">Size</th><th>Status</th><th className="right">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {backups.map((b) => (
                      <tr key={b.id}>
                        <td><strong>{b.database_name}</strong>
                          <div className="tiny faint mono truncate" style={{ maxWidth: 240 }}>{b.filename}</div>
                        </td>
                        <td className="tiny nowrap">{formatDate(b.started_at)}
                          <div className="faint">{timeAgo(b.started_at)}</div>
                        </td>
                        <td><span className="badge">{b.trigger.replace("_", " ")}</span></td>
                        <td className="tiny">
                          {b.provider}
                          {b.encrypted && <div><span className="badge blue">encrypted</span></div>}
                        </td>
                        <td className="right mono tiny">{formatBytes(b.size_bytes)}</td>
                        <td>
                          <span className={`badge ${b.status === "completed" ? "green" : b.status === "failed" ? "red" : "amber"}`}>
                            {b.status}
                          </span>
                          {b.error && <div className="tiny" style={{ color: "var(--danger)", maxWidth: 220 }}>{b.error}</div>}
                        </td>
                        <td className="actions">
                          {b.status === "completed" && (
                            <a className="btn ghost sm" href={api.downloadBackupUrl(b.id)}>Download</a>
                          )}
                          {canWrite && b.status === "completed" && (
                            <button className="btn ghost sm" onClick={() => setRestoring(b)}>Restore</button>
                          )}
                          {canWrite && (
                            <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                              onClick={() => setDeleting(b)}>Delete</button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )
        ) : schedules.length === 0 ? (
          <div className="panel">
            <EmptyState
              icon="⏱" title="No schedules"
              hint="A schedule backs up one database, or all of them, at a fixed interval."
              action={canWrite && (
                <button className="btn primary" onClick={() => setEditingSchedule("new")}>New schedule</button>
              )}
            />
          </div>
        ) : (
          <div className="panel">
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Name</th><th>Target</th><th>Interval</th><th>Retention</th>
                    <th>Last run</th><th>Next run</th><th className="right">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {schedules.map((s) => (
                    <tr key={s.id}>
                      <td>
                        <strong>{s.name}</strong>{" "}
                        {!s.enabled && <span className="badge">paused</span>}
                        {s.encrypt && <div><span className="badge blue">encrypted</span></div>}
                      </td>
                      <td className="tiny">{s.database_name || <em className="faint">all databases</em>}
                        <div className="faint">{s.provider}</div>
                      </td>
                      <td className="tiny">{formatInterval(s.interval_secs)}</td>
                      <td className="tiny faint">
                        {s.retention_count > 0 && <div>keep last {s.retention_count}</div>}
                        {s.retention_days > 0 && <div>keep {s.retention_days} days</div>}
                        {s.retention_count === 0 && s.retention_days === 0 && "keep everything"}
                      </td>
                      <td className="tiny nowrap">
                        {s.last_run_at ? timeAgo(s.last_run_at) : "never"}
                        {s.last_status && (
                          <div>
                            <span className={`badge ${s.last_status === "ok" ? "green" : "red"}`}>{s.last_status}</span>
                          </div>
                        )}
                        {s.last_error && (
                          <div className="tiny" style={{ color: "var(--danger)", maxWidth: 200 }}>{s.last_error}</div>
                        )}
                      </td>
                      <td className="tiny nowrap faint">{s.enabled ? formatDate(s.next_run_at) : "—"}</td>
                      <td className="actions">
                        {canWrite && (
                          <>
                            <button className="btn ghost sm" onClick={() => void runNow(s)} disabled={busy}>Run now</button>
                            <button className="btn ghost sm" onClick={() => setEditingSchedule(s)}>Edit</button>
                            <button className="btn ghost sm" style={{ color: "var(--danger)" }}
                              onClick={() => setDeletingSchedule(s)}>Delete</button>
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

      {running && (
        <RunBackupModal
          databases={databases} providers={providers} encryptionAvailable={encryptionAvailable}
          onClose={() => setRunning(false)}
          onDone={async () => { setRunning(false); await reload(); }}
        />
      )}

      {editingSchedule && (
        <ScheduleModal
          schedule={editingSchedule === "new" ? null : editingSchedule}
          databases={databases} providers={providers} encryptionAvailable={encryptionAvailable}
          onClose={() => setEditingSchedule(null)}
          onSaved={async () => { setEditingSchedule(null); await reload(); }}
        />
      )}

      {restoring && (
        <ConfirmDialog
          title={`Restore ${restoring.database_name}?`}
          danger busy={busy} confirmLabel="Restore now"
          message={
            <>
              <p>
                This replaces the current contents of <strong>{restoring.database_name}</strong> with
                the backup taken {timeAgo(restoring.started_at)}. Any data written since then will be lost.
              </p>
              <p className="faint">
                A safety snapshot of the current database is taken first, so you can undo this by
                restoring that snapshot. The backup's checksum is verified before anything is overwritten.
              </p>
            </>
          }
          onCancel={() => setRestoring(null)}
          onConfirm={() => void confirmRestore()}
        />
      )}

      {deleting && (
        <ConfirmDialog
          title="Delete this backup?" danger busy={busy} confirmLabel="Delete backup"
          message={<p>The backup file is permanently removed from {deleting.provider} storage.</p>}
          onCancel={() => setDeleting(null)}
          onConfirm={() => void confirmDelete()}
        />
      )}

      {deletingSchedule && (
        <ConfirmDialog
          title={`Delete schedule ${deletingSchedule.name}?`} danger busy={busy} confirmLabel="Delete schedule"
          message={<p>No further automatic backups will run. Backups it already created are kept.</p>}
          onCancel={() => setDeletingSchedule(null)}
          onConfirm={() => void confirmDeleteSchedule()}
        />
      )}
    </>
  );
}

function RunBackupModal({
  databases, providers, encryptionAvailable, onClose, onDone,
}: {
  databases: DatabaseMeta[]; providers: string[]; encryptionAvailable: boolean;
  onClose: () => void; onDone: () => void;
}) {
  const toast = useToast();
  const [database, setDatabase] = useState(databases[0]?.name ?? "");
  const [provider, setProvider] = useState(providers[0] ?? "local");
  const [encrypt, setEncrypt] = useState(false);
  const [busy, setBusy] = useState(false);

  async function run() {
    setBusy(true);
    try {
      const rec = await api.createBackup(database, provider, encrypt);
      toast.success(`Backed up ${database} (${formatBytes(rec.size_bytes)})`);
      onDone();
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Back up now"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void run()} disabled={busy || !database}>
            {busy ? <span className="spinner" /> : "Start backup"}
          </button>
        </>
      }
    >
      <Field label="Database">
        <select className="select" value={database} onChange={(e) => setDatabase(e.target.value)}>
          {databases.map((d) => <option key={d.id} value={d.name}>{d.name}</option>)}
        </select>
      </Field>
      <Field label="Storage">
        <select className="select" value={provider} onChange={(e) => setProvider(e.target.value)}>
          {providers.map((p) => <option key={p} value={p}>{p}</option>)}
        </select>
      </Field>
      <Checkbox
        label={encryptionAvailable
          ? "Encrypt this backup"
          : "Encrypt this backup (no encryption key configured)"}
        checked={encrypt}
        disabled={!encryptionAvailable}
        onChange={setEncrypt}
      />
      <p className="tiny faint" style={{ marginTop: 10 }}>
        The snapshot is taken with SQLite's VACUUM INTO, which writes a transactionally consistent
        copy while the database stays online.
      </p>
    </Modal>
  );
}

function ScheduleModal({
  schedule, databases, providers, encryptionAvailable, onClose, onSaved,
}: {
  schedule: BackupSchedule | null;
  databases: DatabaseMeta[]; providers: string[]; encryptionAvailable: boolean;
  onClose: () => void; onSaved: () => void;
}) {
  const toast = useToast();
  const isNew = schedule === null;

  const [name, setName] = useState(schedule?.name ?? "Nightly backup");
  const [databaseId, setDatabaseId] = useState(schedule?.database_id ?? "");
  const [hours, setHours] = useState(schedule ? schedule.interval_secs / 3600 : 24);
  const [provider, setProvider] = useState(schedule?.provider ?? providers[0] ?? "local");
  const [encrypt, setEncrypt] = useState(schedule?.encrypt ?? false);
  const [retentionCount, setRetentionCount] = useState(schedule?.retention_count ?? 7);
  const [retentionDays, setRetentionDays] = useState(schedule?.retention_days ?? 0);
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    try {
      const body = {
        name: name.trim(),
        database_id: databaseId,
        interval_secs: Math.round(Number(hours) * 3600),
        provider,
        encrypt,
        retention_count: Number(retentionCount) || 0,
        retention_days: Number(retentionDays) || 0,
        enabled,
      };
      if (isNew) {
        await api.createSchedule(body);
        toast.success(`Created ${name}`);
      } else {
        await api.updateSchedule(schedule!.id, body);
        toast.success(`Updated ${name}`);
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
      title={isNew ? "New backup schedule" : `Edit ${schedule!.name}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn primary" onClick={() => void save()} disabled={busy || !name.trim()}>
            {busy ? <span className="spinner" /> : isNew ? "Create schedule" : "Save changes"}
          </button>
        </>
      }
    >
      <Field label="Name">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>

      <Field label="Database" hint="Leave as 'All databases' to include databases created later.">
        <select className="select" value={databaseId} onChange={(e) => setDatabaseId(e.target.value)}>
          <option value="">All databases</option>
          {databases.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
        </select>
      </Field>

      <div className="grid-2">
        <Field label="Run every (hours)" hint="Minimum 5 minutes (0.084 hours).">
          <input className="input" type="number" min={0.084} step={0.5} value={hours}
            onChange={(e) => setHours(Number(e.target.value))} />
        </Field>
        <Field label="Storage">
          <select className="select" value={provider} onChange={(e) => setProvider(e.target.value)}>
            {providers.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </Field>
      </div>

      <div className="grid-2">
        <Field label="Keep last N backups" hint="0 keeps every backup.">
          <input className="input" type="number" min={0} value={retentionCount}
            onChange={(e) => setRetentionCount(Number(e.target.value))} />
        </Field>
        <Field label="Keep for N days" hint="0 disables age-based cleanup.">
          <input className="input" type="number" min={0} value={retentionDays}
            onChange={(e) => setRetentionDays(Number(e.target.value))} />
        </Field>
      </div>

      <div className="row-flex">
        <Checkbox label="Enabled" checked={enabled} onChange={setEnabled} />
        <Checkbox
          label={encryptionAvailable ? "Encrypt backups" : "Encrypt backups (no key configured)"}
          checked={encrypt} disabled={!encryptionAvailable} onChange={setEncrypt}
        />
      </div>
      <p className="tiny faint" style={{ marginTop: 8 }}>
        Retention never removes the most recent successful backup, so a database always keeps at
        least one recovery point.
      </p>
    </Modal>
  );
}
