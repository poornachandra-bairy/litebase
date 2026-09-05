import { useCallback, useEffect, useState } from "react";

// Three states rather than two: "system" follows the reader's operating system,
// and the two explicit choices override it. That matters on an e-reader-style
// interface, where someone may well want the page light while the rest of their
// desktop is dark.

export type Theme = "light" | "dark" | "system";

const STORAGE_KEY = "litebase.theme";

function readStored(): Theme {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    if (value === "light" || value === "dark" || value === "system") return value;
  } catch {
    // Private browsing or blocked storage; fall back to following the system.
  }
  return "system";
}

/** Writes the choice onto the document root, where the stylesheet reads it. */
function apply(theme: Theme) {
  const root = document.documentElement;
  if (theme === "system") {
    // Removing the attribute hands control back to the prefers-color-scheme
    // media query rather than pinning a palette.
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", theme);
  }
}

// Applied before React mounts so the first paint is already in the right
// palette, which avoids a flash of the wrong background on load.
apply(readStored());

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(readStored);

  useEffect(() => {
    apply(theme);
    try {
      localStorage.setItem(STORAGE_KEY, theme);
    } catch {
      // Losing the preference is not worth failing the render over.
    }
  }, [theme]);

  /** Cycles light, then dark, then back to following the system. */
  const cycle = useCallback(() => {
    setTheme((cur) => (cur === "light" ? "dark" : cur === "dark" ? "system" : "light"));
  }, []);

  return { theme, setTheme, cycle };
}

/** Describes the current setting for a tooltip. */
export function themeLabel(theme: Theme): string {
  switch (theme) {
    case "light":
      return "Light — click for dark";
    case "dark":
      return "Dark — click to follow your system";
    default:
      return "Following your system — click for light";
  }
}

/** A glyph for each state: sun, moon, and half-filled for automatic. */
export function themeIcon(theme: Theme): string {
  switch (theme) {
    case "light":
      return "☀";
    case "dark":
      return "☾";
    default:
      return "◐";
  }
}
