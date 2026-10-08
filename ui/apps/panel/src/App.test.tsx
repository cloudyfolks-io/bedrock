import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "./App";
import { createFakeApi } from "./test/fakeApi";
import { defaultSession } from "./test/renderPanel";

describe("App", () => {
  afterEach(() => {
    localStorage.clear();
    window.history.pushState({}, "", "/");
  });

  it("TestAppRendersTheShellTitle", async () => {
    createFakeApi({
      "GET /proxy/v1/session": { body: defaultSession },
      "GET /proxy/v1/projects": { body: { projects: [] } },
    }).install();
    render(<App />);
    expect(await screen.findByText("Bedrock")).toBeTruthy();
    expect(document.documentElement.dir).toBe("ltr");
  });
});
