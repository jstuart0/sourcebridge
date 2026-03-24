export type ThemeMode = "dark" | "light";
export type UiMode = "editorial" | "glass" | "control";
export type ProductEdition = "oss" | "enterprise";

export const THEME_STORAGE_KEY = "sourcebridge_theme";
export const UI_MODE_STORAGE_KEY = "sourcebridge_ui_mode";

export const DEFAULT_THEME: ThemeMode = "dark";
export const DEFAULT_UI_MODE: UiMode = "editorial";
export const ENTERPRISE_DEFAULT_UI_MODE: UiMode = "control";

const VALID_THEMES = new Set<ThemeMode>(["dark", "light"]);
const VALID_UI_MODES = new Set<UiMode>(["editorial", "glass", "control"]);

export function isThemeMode(value: string | null | undefined): value is ThemeMode {
  return !!value && VALID_THEMES.has(value as ThemeMode);
}

export function isUiMode(value: string | null | undefined): value is UiMode {
  return !!value && VALID_UI_MODES.has(value as UiMode);
}

export function normalizeThemeMode(value: string | null | undefined): ThemeMode {
  return isThemeMode(value) ? value : DEFAULT_THEME;
}

export function normalizeUiMode(value: string | null | undefined): UiMode {
  return isUiMode(value) ? value : DEFAULT_UI_MODE;
}

export function getUiModeStorageKey(edition: ProductEdition) {
  return `${UI_MODE_STORAGE_KEY}_${edition}`;
}

export function getDefaultUiMode(edition: ProductEdition): UiMode {
  return edition === "enterprise" ? ENTERPRISE_DEFAULT_UI_MODE : DEFAULT_UI_MODE;
}
