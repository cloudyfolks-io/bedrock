import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Alert } from "./Alert";
import { OtpField } from "./OtpField";
import { TextField } from "./TextField";

describe("TextField", () => {
  it("binds the label to the input", () => {
    render(<TextField label="Username" name="username" value="" onChange={() => {}} />);
    const input = screen.getByLabelText("Username");
    expect(input.tagName).toBe("INPUT");
  });
});

describe("OtpField", () => {
  it("accepts only digits", () => {
    const onChange = vi.fn();
    render(<OtpField length={6} value="" onChange={onChange} label="Code" />);
    const first = screen.getByLabelText("Code 1");
    fireEvent.change(first, { target: { value: "a" } });
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.change(first, { target: { value: "5" } });
    expect(onChange).toHaveBeenCalledWith("5");
  });

  it("distributes a six-digit paste across the fields", () => {
    const onChange = vi.fn();
    render(<OtpField length={6} value="" onChange={onChange} label="Code" />);
    const first = screen.getByLabelText("Code 1");
    const clipboardData = { getData: () => "123456" };
    fireEvent.paste(first, { clipboardData });
    expect(onChange).toHaveBeenCalledWith("123456");
  });
});

describe("Alert", () => {
  it("carries role alert for the error tone", () => {
    render(<Alert tone="error">Failed</Alert>);
    expect(screen.getByRole("alert").textContent).toBe("Failed");
  });

  it("carries role status for the info tone", () => {
    render(<Alert tone="info">Saved</Alert>);
    expect(screen.getByRole("status").textContent).toBe("Saved");
  });
});
