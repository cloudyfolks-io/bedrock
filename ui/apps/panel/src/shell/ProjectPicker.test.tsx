import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { LocaleProvider } from "../LocaleProvider";
import { ProjectPicker } from "./ProjectPicker";

it("keeps the translated project picker and its portaled options in RTL", async () => {
  const user = userEvent.setup();
  const onProjectChange = vi.fn();
  const projects = [
    { name: "db", displayName: "Database", tenant: "acme", namespace: "acme-db" },
    { name: "web", displayName: "وب", tenant: "acme", namespace: "acme-web" },
  ];
  const { container } = render(
    <LocaleProvider initial="fa">
      <ProjectPicker projects={projects} project="db" onProjectChange={onProjectChange} />
    </LocaleProvider>,
  );

  await user.click(screen.getByRole("combobox", { name: "پروژه" }));
  const list = await screen.findByRole("listbox", { name: "پروژه" });
  expect(container.contains(list)).toBe(false);
  expect(list.closest("[dir]")?.getAttribute("dir")).toBe("rtl");
  expect(within(list).getByRole("option", { name: "Database", selected: true })).toBeTruthy();
  await user.click(within(list).getByRole("option", { name: "وب" }));
  expect(onProjectChange).toHaveBeenCalledWith("web");
});
