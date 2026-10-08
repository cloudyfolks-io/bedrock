import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DirectionProvider } from "@base-ui/react/direction-provider";
import { describe, expect, it, vi } from "vitest";
import {
  Badge,
  Checkbox,
  CodeTextarea,
  NumberField,
  Progress,
  Select,
  Switch,
  Tab,
  TabList,
  TabPanel,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  Tabs,
  Textarea,
  ThemeProvider,
} from "./index";

describe("Table", () => {
  it("renders the caption and start-aligned cells", () => {
    render(
      <Table caption="Virtual machines">
        <TableBody>
          <TableRow>
            <TableCell>web-1</TableCell>
          </TableRow>
        </TableBody>
      </Table>,
    );
    expect(screen.getByText("Virtual machines").tagName).toBe("CAPTION");
    expect(screen.getByText("web-1").className).toContain("br-td");
  });

  it("sorts from a header by click and by Enter", async () => {
    const onSort = vi.fn();
    const user = userEvent.setup();
    render(
      <Table caption="t">
        <TableHead>
          <TableRow>
            <TableHeaderCell sort="ascending" onSort={onSort}>
              Name
            </TableHeaderCell>
          </TableRow>
        </TableHead>
      </Table>,
    );
    expect(screen.getByRole("columnheader").getAttribute("aria-sort")).toBe("ascending");
    await user.click(screen.getByRole("button", { name: "Name" }));
    expect(onSort).toHaveBeenCalledTimes(1);
    await user.keyboard("{Enter}");
    expect(onSort).toHaveBeenCalledTimes(2);
  });
});

describe("Badge", () => {
  it.each(["neutral", "success", "warning", "danger", "info"] as const)("sets data-tone %s and keeps the text", (tone) => {
    render(<Badge tone={tone}>Running</Badge>);
    const badge = screen.getByText("Running");
    expect(badge.getAttribute("data-tone")).toBe(tone);
  });
});

function TabsFixture() {
  const [value, setValue] = useState("a");
  return (
    <Tabs value={value} onValueChange={setValue}>
      <TabList>
        <Tab value="a">One</Tab>
        <Tab value="b">Two</Tab>
        <Tab value="c">Three</Tab>
      </TabList>
      <TabPanel value="a">panel-a</TabPanel>
      <TabPanel value="b">panel-b</TabPanel>
      <TabPanel value="c">panel-c</TabPanel>
    </Tabs>
  );
}

describe("Tabs", () => {
  it("shows only the panel of the chosen tab", async () => {
    const user = userEvent.setup();
    render(<TabsFixture />);
    expect(screen.getByText("panel-a")).toBeTruthy();
    await user.click(screen.getByRole("tab", { name: "Two" }));
    expect(screen.queryByText("panel-a")).toBeNull();
    expect(screen.getByText("panel-b")).toBeTruthy();
  });

  it("moves focus with the arrow keys", async () => {
    const user = userEvent.setup();
    render(<TabsFixture />);
    await user.click(screen.getByRole("tab", { name: "One" }));
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Two" }));
  });

  it("reverses the arrow keys in a right-to-left tree", async () => {
    const user = userEvent.setup();
    render(
      <DirectionProvider direction="rtl">
        <div dir="rtl">
          <TabsFixture />
        </div>
      </DirectionProvider>,
    );
    await user.click(screen.getByRole("tab", { name: "One" }));
    await user.keyboard("{ArrowLeft}");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Two" }));
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: "One" }));
  });
});

function Toggles({ disabled }: { disabled?: boolean }) {
  const [checked, setChecked] = useState(false);
  const [on, setOn] = useState(false);
  return (
    <>
      <Checkbox label="Accept" checked={checked} onCheckedChange={setChecked} disabled={disabled ?? false} />
      <Switch label="Enabled" checked={on} onCheckedChange={setOn} disabled={disabled ?? false} />
    </>
  );
}

describe("Checkbox and Switch", () => {
  it("toggle from the label and expose aria-checked", async () => {
    const user = userEvent.setup();
    render(<Toggles />);
    const checkbox = screen.getByRole("checkbox", { name: "Accept" });
    const toggle = screen.getByRole("switch", { name: "Enabled" });
    expect(checkbox.getAttribute("aria-checked")).toBe("false");
    await user.click(screen.getByText("Accept"));
    expect(checkbox.getAttribute("aria-checked")).toBe("true");
    await user.click(screen.getByText("Enabled"));
    expect(toggle.getAttribute("aria-checked")).toBe("true");
  });

  it("block changes when disabled", async () => {
    const user = userEvent.setup();
    render(<Toggles disabled />);
    await user.click(screen.getByText("Accept"));
    await user.click(screen.getByText("Enabled"));
    expect(screen.getByRole("checkbox", { name: "Accept" }).getAttribute("aria-checked")).toBe("false");
    expect(screen.getByRole("switch", { name: "Enabled" }).getAttribute("aria-checked")).toBe("false");
  });
});

const options = [
  { value: "a", label: "Alpha" },
  { value: "b", label: "Beta" },
];

describe("Select", () => {
  it("opens with Enter, picks with Enter and shows the label", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    function Fixture() {
      const [value, setValue] = useState("");
      return (
        <Select
          label="Letter"
          value={value}
          onValueChange={(next) => {
            onValueChange(next);
            setValue(next);
          }}
          options={options}
          placeholder="Choose"
        />
      );
    }
    render(<Fixture />);
    const trigger = screen.getByRole("combobox", { name: /Letter/ });
    trigger.focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("option", { name: "Beta" });
    await user.keyboard("{ArrowDown}{Enter}");
    await waitFor(() => expect(onValueChange).toHaveBeenCalled());
    expect(["a", "b"]).toContain(onValueChange.mock.calls[0]?.[0]);
    await waitFor(() => expect(screen.getByRole("combobox", { name: /Letter/ }).textContent).toMatch(/Alpha|Beta/));
  });

  it("opens with ArrowDown", async () => {
    const user = userEvent.setup();
    render(<Select label="Letter" value="" onValueChange={() => {}} options={options} />);
    screen.getByRole("combobox", { name: /Letter/ }).focus();
    await user.keyboard("{ArrowDown}");
    expect(await screen.findByRole("option", { name: "Alpha" })).toBeTruthy();
  });

  it("describes itself by the error text", () => {
    render(<Select label="Letter" value="" onValueChange={() => {}} options={options} error="Required" />);
    const trigger = screen.getByRole("combobox", { name: /Letter/ });
    const describedBy = trigger.getAttribute("aria-describedby") ?? "";
    expect(document.getElementById(describedBy)?.textContent).toBe("Required");
  });
});

describe("Textarea", () => {
  it("binds the label and shows the error", () => {
    render(<Textarea label="Notes" value="" onChange={() => {}} error="Too long" />);
    expect(screen.getByLabelText("Notes").tagName).toBe("TEXTAREA");
    expect(screen.getByText("Too long")).toBeTruthy();
  });

  it("makes the code variant left to right without spell check", () => {
    render(<CodeTextarea label="Script" value="" onChange={() => {}} />);
    const area = screen.getByLabelText("Script");
    expect(area.getAttribute("dir")).toBe("ltr");
    expect(area.getAttribute("spellcheck")).toBe("false");
  });
});

describe("NumberField", () => {
  it("reports numbers and null for empty", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    function Fixture() {
      const [value, setValue] = useState<number | null>(null);
      return (
        <NumberField
          label="Count"
          value={value}
          onValueChange={(next) => {
            onValueChange(next);
            setValue(next);
          }}
        />
      );
    }
    render(<Fixture />);
    const input = screen.getByRole("textbox", { name: "Count" });
    await user.type(input, "12");
    expect(onValueChange).toHaveBeenLastCalledWith(12);
    await user.clear(input);
    expect(onValueChange).toHaveBeenLastCalledWith(null);
  });

  it("clamps to min and max on blur", async () => {
    const user = userEvent.setup();
    function Fixture() {
      const [value, setValue] = useState<number | null>(5);
      return <NumberField label="Count" value={value} onValueChange={setValue} min={1} max={10} />;
    }
    render(<Fixture />);
    const input = screen.getByRole("textbox", { name: "Count" }) as HTMLInputElement;
    await user.clear(input);
    await user.type(input, "99");
    await user.tab();
    expect(input.value).toBe("10");
  });
});

describe("Progress", () => {
  it("is a progressbar with its value", () => {
    render(<Progress label="Disk" value={30} max={120} />);
    const bar = screen.getByRole("progressbar", { name: "Disk" });
    expect(bar.getAttribute("aria-valuenow")).toBe("30");
  });
});

describe("ThemeProvider", () => {
  it("forces the theme when given and ignores the system preference", () => {
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })) as unknown as typeof window.matchMedia;
    render(
      <ThemeProvider theme="dark">
        <span>x</span>
      </ThemeProvider>,
    );
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
