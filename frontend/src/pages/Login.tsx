import { useState } from "react";
import type { FormEvent } from "react";
import { useAuth } from "../lib/auth";
import { ApiError } from "../lib/api";
import { Field } from "../components/common";

export function LoginPage() {
  const { login } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await login(email, password);
    } catch (err) {
      // The server deliberately returns the same message for an unknown
      // address and a wrong password, and it is shown verbatim.
      setError(err instanceof ApiError ? err.message : "Could not sign in. Please try again.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-page">
      <div className="login-card">
        <div className="brand">
          <div className="logo">🗃️</div>
          <h1>Litebase</h1>
          <p>Sign in to manage your databases</p>
        </div>

        <form className="panel" onSubmit={onSubmit}>
          <div className="panel-body">
            <Field label="Email">
              <input
                className="input"
                type="email"
                value={email}
                autoComplete="username"
                autoFocus
                required
                onChange={(e) => setEmail(e.target.value)}
                placeholder="admin@example.com"
              />
            </Field>
            <Field label="Password">
              <input
                className="input"
                type="password"
                value={password}
                autoComplete="current-password"
                required
                onChange={(e) => setPassword(e.target.value)}
                placeholder="••••••••••"
              />
            </Field>

            {error && (
              <div
                className="small"
                role="alert"
                style={{
                  color: "var(--danger)", background: "var(--danger-soft)",
                  border: "1px solid rgba(240,93,93,.3)", borderRadius: 4,
                  padding: "8px 10px", marginBottom: 12,
                }}
              >
                {error}
              </div>
            )}

            <button className="btn primary" type="submit" disabled={busy} style={{ width: "100%" }}>
              {busy ? <span className="spinner" /> : "Sign in"}
            </button>
          </div>
        </form>

        <p className="tiny faint" style={{ textAlign: "center", marginTop: 14 }}>
          First time? The generated password was printed to the server log on first start.
        </p>
      </div>
    </div>
  );
}
