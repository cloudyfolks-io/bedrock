import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Alert } from "./Alert";
import { OtpField } from "./OtpField";
import { TextField } from "./TextField";

function ControlledOtpField({ length, initial, label }: { length: number; initial: string; label: string }) {
  const [value, setValue] = useState(initial);
  return <OtpField length={length} value={value} onChange={setValue} label={label} />;
}

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

  it("clears a filled digit on backspace", () => {
    const onChange = vi.fn();
    render(<OtpField length={6} value="123456" onChange={onChange} label="Code" />);
    const sixth = screen.getByLabelText("Code 6");
    fireEvent.change(sixth, { target: { value: "" } });
    expect(onChange).toHaveBeenCalledWith("12345");
  });

  it("moves focus to the previous box and clears it on backspace when already empty", () => {
    const onChange = vi.fn();
    render(<OtpField length={6} value="12345" onChange={onChange} label="Code" />);
    const sixth = screen.getByLabelText("Code 6");
    fireEvent.keyDown(sixth, { key: "Backspace" });
    expect(onChange).toHaveBeenCalledWith("1234");
    const fifth = screen.getByLabelText("Code 5");
    expect(document.activeElement).toBe(fifth);
  });

  it("never snaps a cleared digit back to its old value after a re-render", () => {
    render(<ControlledOtpField length={6} initial="123456" label="Code" />);
    const sixth = screen.getByLabelText("Code 6") as HTMLInputElement;
    fireEvent.change(sixth, { target: { value: "" } });
    expect(sixth.value).toBe("");
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
