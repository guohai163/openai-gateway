export const themeStorageKey = "gateway-theme";

export function normalizeTheme(value) {
  return value === "light" || value === "dark" ? value : "system";
}

export function readThemePreference() {
  try {
    return normalizeTheme(localStorage.getItem(themeStorageKey));
  } catch {
    return "system";
  }
}

export function resolveTheme(preference, systemDark) {
  const theme = normalizeTheme(preference);
  return theme === "system" ? (systemDark ? "dark" : "light") : theme;
}
