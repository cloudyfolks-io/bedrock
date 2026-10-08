import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";
import type { KubeList } from "../../api/types";
import sshkeys from "../../fixtures/sshkeys.json";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { validateSshKey } from "../../resources/validators";
import { openApiRoutes } from "../../test/openapi";
import { renderPanel } from "../../test/renderPanel";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const base = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/acme-web";

describe("SSH keys", () => {
  it("TestPublicKeyCheck accepts public keys and refuses private keys", () => {
    expect(validateSshKey({ publicKey: "ssh-ed25519 AAAA user@host" })).toEqual([]);
    expect(validateSshKey({ publicKey: "ecdsa-sha2-nistp256 AAAA" })).toEqual([]);
    expect(validateSshKey({ publicKey: "ssh-rsa AAAA" })).toEqual([]);
    expect(validateSshKey({ publicKey: "hello" })).toHaveLength(1);
    expect(validateSshKey({ publicKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nabc" })[0]?.field).toBe("spec.publicKey");
  });

  it("lists keys and refuses a private key in the form", async () => {
    const user = userEvent.setup();
    const def = definitionFor("sshkeys");
    if (!def) {
      throw new Error("missing definition");
    }
    const { api } = renderPanel(
      <Routes>
        <Route path="/p/:project/ssh-keys" element={<ResourceListPage def={def} />} />
      </Routes>,
      { route: "/p/web/ssh-keys", api: { ...openApiRoutes(), "GET /proxy/v1/projects": { body: projects }, [`GET ${base}/sshkeys`]: { body: sshkeys as unknown as KubeList } } },
    );
    expect(await screen.findByRole("link", { name: "laptop" })).toBeTruthy();
    expect(screen.getByText("ssh-ed25519")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Create" }));
    await user.type(await screen.findByLabelText("Name", undefined, { timeout: 15000 }), "bad");
    const area = screen.getByLabelText(/^publicKey/);
    expect(area.tagName).toBe("TEXTAREA");
    await user.click(area);
    await user.paste("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----");
    await user.click(document.querySelector("form button[type=submit]") as HTMLElement);
    expect((await screen.findAllByText(/private key/i)).length).toBeGreaterThan(0);
    expect(api.calls.some((call) => call.method === "POST" && call.path !== "/proxy/v1/authz")).toBe(false);
  });
});
