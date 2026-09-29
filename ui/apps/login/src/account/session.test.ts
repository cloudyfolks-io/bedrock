import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { handleAccountError } from "./session";

describe("handleAccountError", () => {
  it("sends the user to login on a no_session answer, without setting an error", () => {
    const onSessionExpired = vi.fn();
    const setError = vi.fn();
    handleAccountError(new ApiError("no_session"), onSessionExpired, setError);
    expect(onSessionExpired).toHaveBeenCalledTimes(1);
    expect(setError).not.toHaveBeenCalled();
  });

  it("sets the server's error code for any other ApiError", () => {
    const onSessionExpired = vi.fn();
    const setError = vi.fn();
    handleAccountError(new ApiError("rate_limited"), onSessionExpired, setError);
    expect(onSessionExpired).not.toHaveBeenCalled();
    expect(setError).toHaveBeenCalledWith("rate_limited");
  });

  it("falls back to unknown for a non-ApiError rejection", () => {
    const onSessionExpired = vi.fn();
    const setError = vi.fn();
    handleAccountError(new Error("network down"), onSessionExpired, setError);
    expect(onSessionExpired).not.toHaveBeenCalled();
    expect(setError).toHaveBeenCalledWith("unknown");
  });
});
