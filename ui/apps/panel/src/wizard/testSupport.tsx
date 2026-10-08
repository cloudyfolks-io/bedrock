import { screen } from "@testing-library/react";
import type userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router";
import type { ReactElement } from "react";
import type { Check, KubeList } from "../api/types";
import clusterimages from "../fixtures/clusterimages.json";
import images from "../fixtures/images.json";
import securitygroups from "../fixtures/securitygroups.json";
import sshkeys from "../fixtures/sshkeys.json";
import subnets from "../fixtures/subnets.json";
import virtualmachines from "../fixtures/virtualmachines.json";
import volumes from "../fixtures/volumes.json";
import type { FakeRoute } from "../test/fakeApi";
import { renderPanel, type RenderOptions } from "../test/renderPanel";

const projects = { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] };
const root = "/apis/bedrock.cloudyfolks.io/v1alpha1";
export const ns = `${root}/namespaces/acme-web`;
const asList = (value: unknown) => value as KubeList;

function Path() {
  const location = useLocation();
  return <span data-testid="path">{location.pathname + location.search}</span>;
}

export function wizardApi(extra: Record<string, FakeRoute> = {}): Record<string, FakeRoute> {
  return {
    "GET /proxy/v1/projects": { body: projects },
    [`GET ${ns}/images`]: { body: asList(images) },
    [`GET ${root}/clusterimages`]: { body: asList(clusterimages) },
    [`GET ${ns}/virtualmachines`]: { body: asList(virtualmachines) },
    [`GET ${ns}/subnets`]: { body: asList(subnets) },
    [`GET ${ns}/securitygroups`]: { body: asList(securitygroups) },
    [`GET ${ns}/sshkeys`]: { body: asList(sshkeys) },
    [`GET ${ns}/volumes`]: { body: asList(volumes) },
    "GET /apis/instancetype.kubevirt.io/v1beta1/virtualmachineclusterinstancetypes": { status: 403, body: { error: "forbidden" } },
    [`GET ${root}/projects/web`]: { body: { metadata: { name: "web" }, status: { namespace: "acme-web", subnet: "default" } } },
    ...extra,
  };
}

export function renderWizard(element: ReactElement, path: string, options: { api?: Record<string, FakeRoute>; allow?: (check: Check) => boolean; locale?: RenderOptions["locale"] } = {}) {
  return renderPanel(
    <>
      <Routes>
        <Route path={path} element={element} />
        <Route path="*" element={<span>elsewhere</span>} />
      </Routes>
      <Path />
    </>,
    { route: path.replace(":project", "web"), ...(options.allow ? { allow: options.allow } : {}), ...(options.locale ? { locale: options.locale } : {}), api: wizardApi(options.api) },
  );
}

export async function choose(user: ReturnType<typeof userEvent.setup>, label: RegExp | string, option: RegExp | string) {
  await user.click(await screen.findByRole("combobox", { name: label }));
  await user.click(await screen.findByRole("option", { name: option }));
}
