import { useState } from "react";
import { Modal } from "./Modal";
import { Field } from "./common";
import { MethodBadge } from "./common";
import type { Endpoint } from "../lib/types";

/**
 * The API explorer. It builds a request against the live endpoint and shows the
 * exact response, so an operator can confirm behaviour and copy a working curl
 * command.
 */
export function EndpointTester({ endpoint, onClose }: { endpoint: Endpoint; onClose: () => void }) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [apiKey, setApiKey] = useState("");
  const [body, setBody] = useState("{}");
  const [method, setMethod] = useState(endpoint.kind === "query" ? endpoint.method : "GET");
  const [response, setResponse] = useState<{ status: number; text: string; ms: number } | null>(null);
  const [busy, setBusy] = useState(false);

  const isCrud = endpoint.kind === "crud";
  const pathParams = endpoint.params.filter((p) => p.in === "path");
  const queryParams = endpoint.params.filter((p) => p.in === "query");
  const bodyParams = endpoint.params.filter((p) => p.in === "body");

  /** Substitutes path parameters and appends the query string. */
  function buildUrl(): string {
    let p = endpoint.path;
    for (const param of pathParams) {
      p = p.replace(`{${param.name}}`, encodeURIComponent(values[param.name] ?? ""));
    }
    if (isCrud && values.__id) {
      p = `${p}/${encodeURIComponent(values.__id)}`;
    }

    const search = new URLSearchParams();
    for (const param of queryParams) {
      const v = values[param.name];
      if (v) search.set(param.name, v);
    }
    if (isCrud) {
      for (const key of ["limit", "offset", "sort", "search"]) {
        if (values[key]) search.set(key, values[key]);
      }
    }
    const qs = search.toString();
    return `/api${p}${qs ? `?${qs}` : ""}`;
  }

  const url = buildUrl();
  const sendsBody = ["POST", "PUT", "PATCH"].includes(method);

  async function send() {
    setBusy(true);
    const started = performance.now();
    try {
      const headers: Record<string, string> = {};
      if (apiKey) headers["Authorization"] = `Bearer ${apiKey}`;
      if (sendsBody) headers["Content-Type"] = "application/json";

      const res = await fetch(url, {
        method,
        headers,
        body: sendsBody ? body : undefined,
      });
      const text = await res.text();
      let pretty = text;
      try {
        pretty = JSON.stringify(JSON.parse(text), null, 2);
      } catch {
        // A non-JSON response is shown verbatim.
      }
      setResponse({ status: res.status, text: pretty, ms: Math.round(performance.now() - started) });
    } catch (err) {
      setResponse({
        status: 0,
        text: err instanceof Error ? err.message : "The request failed",
        ms: Math.round(performance.now() - started),
      });
    } finally {
      setBusy(false);
    }
  }

  const curl = [
    `curl -X ${method} \\`,
    `  '${window.location.origin}${url}' \\`,
    apiKey ? `  -H 'Authorization: Bearer ${apiKey}' \\` : `  -H 'Authorization: Bearer YOUR_API_KEY' \\`,
    sendsBody ? `  -H 'Content-Type: application/json' \\` : null,
    sendsBody ? `  -d '${body.replace(/\n\s*/g, "")}'` : null,
  ].filter(Boolean).join("\n").replace(/\\\n$/, "");

  return (
    <Modal
      title={`Test ${endpoint.name}`}
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>Close</button>
          <button className="btn primary" onClick={() => void send()} disabled={busy}>
            {busy ? <span className="spinner" /> : "Send request"}
          </button>
        </>
      }
    >
      <div className="row-flex" style={{ marginBottom: 12 }}>
        {isCrud ? (
          <select className="select" style={{ maxWidth: 120 }} value={method}
            onChange={(e) => setMethod(e.target.value)}>
            {["GET", "POST", "PATCH", "DELETE"].map((m) => <option key={m} value={m}>{m}</option>)}
          </select>
        ) : (
          <MethodBadge method={method} />
        )}
        <code className="tiny truncate" style={{ flex: 1 }}>{url}</code>
      </div>

      <Field label="API key" hint={endpoint.public
        ? "This endpoint is public, so a key is optional."
        : "Required. Paste a key from the API Keys page."}>
        <input className="input mono" type="password" value={apiKey} placeholder="lbk_…"
          onChange={(e) => setApiKey(e.target.value)} />
      </Field>

      {isCrud && ["GET", "PATCH", "DELETE"].includes(method) && (
        <Field label="Row id" hint="Leave blank for the list operation.">
          <input className="input mono" value={values.__id ?? ""}
            onChange={(e) => setValues({ ...values, __id: e.target.value })} />
        </Field>
      )}

      {pathParams.map((p) => (
        <Field key={p.name} label={`${p.name} (path)`}>
          <input className="input mono" value={values[p.name] ?? ""}
            onChange={(e) => setValues({ ...values, [p.name]: e.target.value })} />
        </Field>
      ))}

      {queryParams.map((p) => (
        <Field key={p.name} label={`${p.name} (${p.type}${p.required ? ", required" : ""})`}
          hint={p.description}>
          <input className="input mono" value={values[p.name] ?? ""}
            placeholder={p.default !== undefined ? `default ${String(p.default)}` : ""}
            onChange={(e) => setValues({ ...values, [p.name]: e.target.value })} />
        </Field>
      ))}

      {isCrud && method === "GET" && (
        <div className="grid-2">
          <Field label="limit"><input className="input" value={values.limit ?? ""}
            onChange={(e) => setValues({ ...values, limit: e.target.value })} /></Field>
          <Field label="sort" hint="e.g. -created_at"><input className="input" value={values.sort ?? ""}
            onChange={(e) => setValues({ ...values, sort: e.target.value })} /></Field>
        </div>
      )}

      {(sendsBody || bodyParams.length > 0) && (
        <Field label="Request body (JSON)">
          <textarea className="textarea" rows={5} value={body} spellCheck={false}
            onChange={(e) => setBody(e.target.value)} />
        </Field>
      )}

      {response && (
        <div className="field">
          <label>
            Response{" "}
            <span className={`badge ${response.status >= 200 && response.status < 300 ? "green" : "red"}`}>
              {response.status || "network error"}
            </span>{" "}
            <span className="faint tiny">{response.ms} ms</span>
          </label>
          <pre className="code-block" style={{ maxHeight: 260 }}>{response.text}</pre>
        </div>
      )}

      <div className="field">
        <label>curl</label>
        <pre className="code-block">{curl}</pre>
      </div>
    </Modal>
  );
}
