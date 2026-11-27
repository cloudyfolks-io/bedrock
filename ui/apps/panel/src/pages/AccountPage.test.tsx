import { screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { renderPanel } from "../test/renderPanel";
import { AccountPage } from "./AccountPage";

it("shows language and downloads without a theme choice", () => {
  renderPanel(<AccountPage />);

  expect(screen.getByRole("heading", { level: 1, name: "Account" })).toBeTruthy();
  expect(screen.getAllByRole("combobox")).toEqual([screen.getByRole("combobox", { name: "Language" })]);
  const rows = within(screen.getByRole("region", { name: "Downloads" })).getAllByRole("listitem");
  expect(rows).toHaveLength(2);
  const kubeconfig = within(rows[0]!).getByRole("link", { name: "Download kubeconfig", description: /Configure kubectl/ });
  const ca = within(rows[1]!).getByRole("link", { name: "Download CA certificate", description: /trust TLS connections/ });
  expect(kubeconfig.getAttribute("href")).toBe("/kubeconfig");
  expect(ca.getAttribute("href")).toBe("/ca.crt");
  expect([kubeconfig, ca].every((link) => link.hasAttribute("download"))).toBe(true);
});
