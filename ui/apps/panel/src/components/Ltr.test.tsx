import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Ltr } from "./Ltr";

describe("Ltr", () => {
  it("TestLtrIsolatesText", () => {
    document.documentElement.dir = "rtl";
    render(<Ltr>10.100.0.5/24</Ltr>);
    const element = screen.getByText("10.100.0.5/24");
    expect(element.tagName).toBe("BDI");
    expect(element.getAttribute("dir")).toBe("ltr");
    expect(element.className).toContain("br-ltr");
    document.documentElement.dir = "ltr";
  });
});
