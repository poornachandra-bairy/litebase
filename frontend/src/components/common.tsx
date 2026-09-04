import type { ReactNode } from "react";

/** Placeholder shown when a list has nothing in it. */
export function EmptyState({
  icon = "◇", title, hint, action,
}: { icon?: string; title: string; hint?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <div className="big">{icon}</div>
      <p style={{ color: "var(--text-dim)", fontWeight: 500 }}>{title}</p>
      {hint && <p className="tiny">{hint}</p>}
      {action && <div style={{ marginTop: 12 }}>{action}</div>}
    </div>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="loading-page">
      <span className="spinner" /> <span style={{ marginLeft: 8 }}>{label}</span>
    </div>
  );
}

/** A labelled form field with optional hint and validation message. */
export function Field({
  label, hint, error, children,
}: { label: string; hint?: ReactNode; error?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {error ? <div className="error">{error}</div> : hint ? <div className="hint">{hint}</div> : null}
    </div>
  );
}

export function Checkbox({
  label, checked, onChange, disabled,
}: { label: ReactNode; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <label className="check">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span>{label}</span>
    </label>
  );
}

/** Renders the HTTP method as a colour-coded badge. */
export function MethodBadge({ method }: { method: string }) {
  const tone =
    method === "GET" ? "blue" :
    method === "DELETE" ? "red" :
    method === "POST" ? "green" : "amber";
  return <span className={`badge mono ${tone}`}>{method || "REST"}</span>;
}

export function StatCard({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {sub && <div className="sub">{sub}</div>}
    </div>
  );
}
