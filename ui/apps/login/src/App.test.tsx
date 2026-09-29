import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

vi.mock("./api", () => ({
  challenge: vi.fn().mockResolvedValue({ type: "username", csrf: "csrf-1" }),
  start: vi.fn().mockResolvedValue({ type: "username", csrf: "csrf-1" }),
}));

describe("App", () => {
  beforeEach(() => {
    window.history.pushState({}, "", "/login/");
  });

  it("switches dir when the language menu changes", async () => {
    render(<App />);
    await waitFor(() => expect(document.documentElement.dir).toBe("ltr"));
    fireEvent.click(screen.getByText("English"));
    fireEvent.click(await screen.findByText("فارسی"));
    await waitFor(() => expect(document.documentElement.dir).toBe("rtl"));
  });
});
