import { describe, expect, it } from "vitest";
import tokens from "../tokens.json";

type Theme = "light" | "dark";
type ColorName = keyof typeof tokens.color;

function channel(value: number): number {
  const scaled = value / 255;
  return scaled <= 0.03928 ? scaled / 12.92 : ((scaled + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
  const red = parseInt(hex.slice(1, 3), 16);
  const green = parseInt(hex.slice(3, 5), 16);
  const blue = parseInt(hex.slice(5, 7), 16);
  return 0.2126 * channel(red) + 0.7152 * channel(green) + 0.0722 * channel(blue);
}

function ratio(foreground: string, background: string): number {
  const [light, dark] = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  return ((light ?? 0) + 0.05) / ((dark ?? 0) + 0.05);
}

function colorOf(name: ColorName, theme: Theme): string {
  return tokens.color[name][theme];
}

const textPairs: [ColorName, ColorName][] = [
  ["text", "background"],
  ["text", "surface"],
  ["textMuted", "background"],
  ["primaryText", "primary"],
  ["danger", "dangerSurface"],
  ["success", "successSurface"],
  ["warning", "warningSurface"],
  ["info", "infoSurface"],
];

const chartColors: ColorName[] = ["chart-1", "chart-2", "chart-3", "chart-4"];

describe("contrast", () => {
  for (const theme of ["light", "dark"] as const) {
    it.each(textPairs)(`TestContrastRatios text %s on %s in ${theme}`, (foreground, background) => {
      expect(ratio(colorOf(foreground, theme), colorOf(background, theme))).toBeGreaterThanOrEqual(4.5);
    });

    it.each(chartColors)(`TestContrastRatios %s on the background in ${theme}`, (name) => {
      expect(ratio(colorOf(name, theme), colorOf("background", theme))).toBeGreaterThanOrEqual(3);
    });
  }
});
