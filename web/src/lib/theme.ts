// LCH-based theme engine
// Generates full token sets from base hue + accent hue + contrast

export interface ThemeConfig {
  baseHue: number;
  accentHue: number;
  contrast: "normal" | "high";
}

export const defaultDarkTheme: ThemeConfig = {
  baseHue: 0,
  accentHue: 250,
  contrast: "normal",
};

export const defaultLightTheme: ThemeConfig = {
  baseHue: 0,
  accentHue: 250,
  contrast: "normal",
};
