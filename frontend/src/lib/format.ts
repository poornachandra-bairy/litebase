import type { Cell } from "./types";

/** Renders a byte count for humans. */
export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, i);
  return `${value >= 100 || i === 0 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

export function formatNumber(n: number): string {
  return new Intl.NumberFormat().format(n);
}

/** Formats a timestamp in the viewer's locale, or "—" when absent. */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString(undefined, {
    year: "numeric", month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit",
  });
}

/** Renders a timestamp as a coarse relative time, e.g. "5m ago". */
export function timeAgo(iso: string | null | undefined): string {
  if (!iso) return "never";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "never";

  const seconds = Math.floor((Date.now() - then) / 1000);
  if (seconds < 0) return "in the future";
  if (seconds < 60) return "just now";

  const steps: [number, string][] = [
    [60, "m"], [3600, "h"], [86400, "d"], [604800, "w"],
  ];
  for (let i = steps.length - 1; i >= 0; i--) {
    const [size, label] = steps[i];
    if (seconds >= size) return `${Math.floor(seconds / size)}${label} ago`;
  }
  return "just now";
}

/** Formats a duration given in seconds as a readable interval. */
export function formatInterval(seconds: number): string {
  if (seconds % 86400 === 0) {
    const d = seconds / 86400;
    return d === 1 ? "every day" : `every ${d} days`;
  }
  if (seconds % 3600 === 0) {
    const h = seconds / 3600;
    return h === 1 ? "every hour" : `every ${h} hours`;
  }
  const m = Math.round(seconds / 60);
  return m === 1 ? "every minute" : `every ${m} minutes`;
}

export function formatDuration(ms: number): string {
  if (ms < 1) return "<1 ms";
  if (ms < 1000) return `${ms.toFixed(ms < 10 ? 2 : 0)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

/** True when a cell holds the tagged binary form. */
export function isBlob(v: Cell): v is { $type: "blob"; base64: string; size: number } {
  return typeof v === "object" && v !== null && "$type" in v && (v as { $type: string }).$type === "blob";
}

/** Renders a cell as a single-line string for a table view. */
export function cellPreview(v: Cell, maxLen = 160): string {
  if (v === null || v === undefined) return "NULL";
  if (isBlob(v)) return `BLOB (${formatBytes(v.size)})`;
  if (typeof v === "boolean") return v ? "true" : "false";

  const s = String(v);
  // Newlines would break the row layout, so they are shown as escapes.
  const flat = s.replace(/\n/g, "\\n").replace(/\r/g, "");
  return flat.length > maxLen ? flat.slice(0, maxLen) + "…" : flat;
}

/** Converts a cell to text for an editable input. */
export function cellToInput(v: Cell): string {
  if (v === null || v === undefined) return "";
  if (isBlob(v)) return v.base64;
  return String(v);
}
