import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useProjects } from "./projects";
import { renderPanel } from "../test/renderPanel";

function Probe() {
  const { data } = useProjects();
  return <p>{data?.map((project) => project.namespace).join(",")}</p>;
}

describe("useProjects", () => {
  it("TestUseProjectsReadsTheEndpoint", async () => {
    const { api } = renderPanel(<Probe />, {
      api: { "GET /proxy/v1/projects": { body: { projects: [{ name: "web", displayName: "Web", tenant: "acme", namespace: "acme-web" }] } } },
    });
    expect(await screen.findByText("acme-web")).toBeTruthy();
    expect(api.calls.filter((call) => call.path === "/proxy/v1/projects")).toHaveLength(1);
  });
});
