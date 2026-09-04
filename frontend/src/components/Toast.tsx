import { createContext, useCallback, useContext, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { ApiError } from "../lib/api";

type ToastKind = "info" | "success" | "error";

interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

interface ToastApi {
  notify: (message: string, kind?: ToastKind) => void;
  success: (message: string) => void;
  /** Renders any thrown value as a message, so callers can pass a caught error. */
  error: (err: unknown, fallback?: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

let nextId = 1;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const dismiss = useCallback((id: number) => {
    setToasts((cur) => cur.filter((t) => t.id !== id));
  }, []);

  const notify = useCallback(
    (message: string, kind: ToastKind = "info") => {
      const id = nextId++;
      setToasts((cur) => [...cur, { id, kind, message }]);
      // Errors stay longer, since they usually need reading.
      window.setTimeout(() => dismiss(id), kind === "error" ? 8000 : 4000);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      notify,
      success: (m) => notify(m, "success"),
      error: (err, fallback = "Something went wrong") => {
        let message = fallback;
        if (err instanceof ApiError) {
          message = err.message;
          // Field-level problems are far more useful than the summary alone.
          const fields = Object.entries(err.fields);
          if (fields.length > 0) {
            message += ": " + fields.map(([k, v]) => `${k} ${v}`).join(", ");
          }
        } else if (err instanceof Error) {
          message = err.message;
        }
        notify(message, "error");
      },
    }),
    [notify],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="toasts">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`}>
            <span className="msg">{t.message}</span>
            <button className="close" onClick={() => dismiss(t.id)} aria-label="Dismiss">
              ×
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used inside a ToastProvider");
  return ctx;
}
