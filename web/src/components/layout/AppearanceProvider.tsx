"use client";

import {
  createContext,
  ReactNode,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  DEFAULT_THEME,
  getDefaultUiMode,
  getUiModeStorageKey,
  isUiMode,
  normalizeThemeMode,
  THEME_STORAGE_KEY,
  type ProductEdition,
  type ThemeMode,
  type UiMode,
} from "@/lib/ui-mode";

type AppearanceContextValue = {
  theme: ThemeMode;
  uiMode: UiMode;
  setTheme: (theme: ThemeMode) => void;
  setUiMode: (mode: UiMode) => void;
};

const AppearanceContext = createContext<AppearanceContextValue>({
  theme: DEFAULT_THEME,
  uiMode: getDefaultUiMode("oss"),
  setTheme: () => {},
  setUiMode: () => {},
});

function applyAppearance(theme: ThemeMode, uiMode: UiMode, edition: ProductEdition) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.uiMode = uiMode;
  document.documentElement.dataset.edition = edition;
}

export function useAppearance() {
  return useContext(AppearanceContext);
}

export function useTheme() {
  const { theme, setTheme } = useAppearance();
  return { theme, setTheme };
}

export function AppearanceProvider({ children }: { children: ReactNode }) {
  const edition: ProductEdition =
    process.env.NEXT_PUBLIC_EDITION === "enterprise" ? "enterprise" : "oss";
  const [theme, setTheme] = useState<ThemeMode>(DEFAULT_THEME);
  const [uiMode, setUiMode] = useState<UiMode>(getDefaultUiMode(edition));

  useEffect(() => {
    const savedTheme = normalizeThemeMode(localStorage.getItem(THEME_STORAGE_KEY));
    const storedUiMode = localStorage.getItem(getUiModeStorageKey(edition));
    const initialUiMode = isUiMode(storedUiMode) ? storedUiMode : getDefaultUiMode(edition);

    setTheme(savedTheme);
    setUiMode(initialUiMode);
    applyAppearance(savedTheme, initialUiMode, edition);
  }, [edition]);

  useEffect(() => {
    applyAppearance(theme, uiMode, edition);
    localStorage.setItem(THEME_STORAGE_KEY, theme);
    localStorage.setItem(getUiModeStorageKey(edition), uiMode);
  }, [edition, theme, uiMode]);

  const value = useMemo(
    () => ({
      theme,
      uiMode,
      setTheme,
      setUiMode,
    }),
    [theme, uiMode]
  );

  return <AppearanceContext.Provider value={value}>{children}</AppearanceContext.Provider>;
}
