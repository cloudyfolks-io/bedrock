import { readFileSync, readdirSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { renderTokens, type Tokens } from "../scripts/tokens";
import tokens from "../tokens.json";

const physicalProperty = /(?<![a-z-])(margin|padding|border)-(left|right)\b|(?<![a-z-])(left|right)\s*:|text-align:\s*(left|right)/;

describe("renderTokens", () => {
  it("matches the committed tokens.css", () => {
    const committed = readFileSync(new URL("./tokens.css", import.meta.url), "utf8");
    expect(committed).toBe(renderTokens(tokens as Tokens));
  });

  it("defines every color token for light and dark", () => {
    const css = renderTokens(tokens as Tokens);
    const lightBlock = css.slice(css.indexOf(":root {"), css.indexOf("}") + 1);
    const darkBlock = css.slice(css.indexOf('[data-theme="dark"]'));
    for (const name of Object.keys(tokens.color)) {
      const property = `--br-color-${name.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
      expect(lightBlock).toContain(property);
      expect(darkBlock).toContain(property);
    }
  });

  it("never uses a physical CSS property", () => {
    for (const file of readdirSync(new URL(".", import.meta.url))) {
      if (!file.endsWith(".css")) {
        continue;
      }
      const css = readFileSync(new URL(file, import.meta.url), "utf8");
      expect(physicalProperty.test(css), `${file} uses a physical property`).toBe(false);
    }
  });
});
