import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./App";

describe("App", () => {
  it("TestAppRendersTheShellTitle", async () => {
    render(<App />);
    expect(await screen.findByText("Bedrock")).toBeTruthy();
    expect(document.documentElement.dir).toBe("ltr");
  });
});
