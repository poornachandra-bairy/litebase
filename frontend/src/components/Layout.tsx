import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { NavLink, useLocation, useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { api } from "../lib/api";
import { useToast } from "./Toast";
import { Modal } from "./Modal";
import { Field } from "./common";
import { themeIcon, themeLabel, useTheme } from "../lib/theme";
import type { DatabaseMeta } from "../lib/types";

/** The dashboard shell: sidebar navigation plus the routed page. */
export function Layout({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth();
  const toast = useToast();
  const location = useLocation();
  const [databases, setDatabases] = useState<DatabaseMeta[]>([]);
  const [showPassword, setShowPassword] = useState(false);
  const { theme, cycle } = useTheme();

  // The sidebar lists databases, so it reloads whenever navigation might have
  // created or removed one.
  useEffect(() => {
    let cancelled = false;
    api
      .listDatabases()
      .then((r) => {
        if (!cancelled) setDatabases(r.databases);
      })
      .catch(() => {
        // A failure here should not block the page the operator asked for.
      });
    return () => {
      cancelled = true;
    };
  }, [location.pathname]);

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <span className="logo">🗃️</span>
          <span>Litebase</span>
        </div>

        <nav className="sidebar-nav">
          <NavItem to="/" icon="▤" label="Dashboard" end />

          <div className="nav-section">
            <div className="nav-label">Data</div>
            <NavItem to="/databases" icon="🗄" label="Databases" count={databases.length} />
            {databases.slice(0, 8).map((db) => (
              <NavLink
                key={db.id}
                to={`/databases/${encodeURIComponent(db.name)}`}
                className={({ isActive }) => `nav-item nested${isActive ? " active" : ""}`}
              >
                <span className="icon">•</span>
                <span className="truncate">{db.name}</span>
              </NavLink>
            ))}
            {databases.length > 8 && (
              <div className="nav-item nested faint tiny">+{databases.length - 8} more</div>
            )}
            <NavItem to="/sql" icon="›_" label="SQL Editor" />
          </div>

          <div className="nav-section">
            <div className="nav-label">API</div>
            <NavItem to="/api" icon="⇄" label="API Builder" />
            <NavItem to="/keys" icon="⚿" label="API Keys" />
          </div>

          <div className="nav-section">
            <div className="nav-label">Operations</div>
            <NavItem to="/backups" icon="⭳" label="Backups" />
            <NavItem to="/settings" icon="⚙" label="Settings" />
          </div>
        </nav>

        <div className="sidebar-footer">
          <div className="who truncate">
            {user?.name || user?.email}
            <small>{user?.role}</small>
          </div>
          <button
            className="theme-toggle"
            title={themeLabel(theme)}
            aria-label={themeLabel(theme)}
            onClick={cycle}
          >
            {themeIcon(theme)}
          </button>
          <button
            className="btn ghost sm"
            title="Change password"
            onClick={() => setShowPassword(true)}
          >
            ⚿
          </button>
          <button
            className="btn ghost sm"
            title="Sign out"
            onClick={() => {
              void logout().then(() => toast.notify("Signed out"));
            }}
          >
            ⏻
          </button>
        </div>
      </aside>

      <div className="main">{children}</div>

      {showPassword && <ChangePasswordModal onClose={() => setShowPassword(false)} />}
    </div>
  );
}

function NavItem({
  to, icon, label, count, end,
}: { to: string; icon: string; label: string; count?: number; end?: boolean }) {
  return (
    <NavLink to={to} end={end} className={({ isActive }) => `nav-item${isActive ? " active" : ""}`}>
      <span className="icon">{icon}</span>
      <span>{label}</span>
      {count !== undefined && count > 0 && <span className="count">{count}</span>}
    </NavLink>
  );
}

function ChangePasswordModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);

  const mismatch = confirm !== "" && next !== confirm;
  const tooShort = next !== "" && next.length < 10;

  async function submit() {
    if (mismatch || tooShort || !current || !next) return;
    setBusy(true);
    try {
      await api.changePassword(current, next);
      toast.success("Password changed. Other sessions have been signed out.");
      onClose();
      navigate("/");
    } catch (err) {
      toast.error(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Change password"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button
            className="btn primary"
            onClick={() => void submit()}
            disabled={busy || mismatch || tooShort || !current || !next}
          >
            {busy ? <span className="spinner" /> : "Change password"}
          </button>
        </>
      }
    >
      <Field label="Current password">
        <input className="input" type="password" value={current} autoComplete="current-password"
          onChange={(e) => setCurrent(e.target.value)} />
      </Field>
      <Field label="New password" error={tooShort ? "Use at least 10 characters." : undefined}
        hint="At least 10 characters.">
        <input className="input" type="password" value={next} autoComplete="new-password"
          onChange={(e) => setNext(e.target.value)} />
      </Field>
      <Field label="Confirm new password" error={mismatch ? "The passwords do not match." : undefined}>
        <input className="input" type="password" value={confirm} autoComplete="new-password"
          onChange={(e) => setConfirm(e.target.value)} />
      </Field>
      <p className="tiny faint">
        Changing your password signs out every other session for your account.
      </p>
    </Modal>
  );
}
