import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useLiveList } from "../stream/live";
import type { FakeReply } from "../test/fakeApi";
import { defaultSession, renderPanel } from "../test/renderPanel";
import { sessionKey, useHttp } from "./HttpProvider";
import { kubeClient, listKey, type ResourceRef } from "./kube";
import { usePendingWrites } from "./pendingWrites";
import type { KubeList, KubeObject } from "./types";

const ref: ResourceRef = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", plural: "volumes", namespaced: true };
const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/volumes";
const baseline = (sizeGi: number): KubeObject => ({
  metadata: { name: "disk", namespace: "web", uid: "old-uid" }, spec: { sizeGi }, status: { phase: "Bound" },
});
const list = (...items: KubeObject[]): KubeList<KubeObject> => ({ metadata: { resourceVersion: "10" }, items });

function deferred() {
  let finish!: (reply: FakeReply) => void;
  const result = new Promise<FakeReply>((resolve) => { finish = resolve; });
  return { result, finish };
}

function Fixture({ scoped = false }: { scoped?: boolean }) {
  const http = useHttp();
  const writes = usePendingWrites(http);
  const client = kubeClient(http);
  const current = useLiveList(ref, { namespace: "web", optimisticCreates: true });
  return (
    <>
      <button onClick={() => void client.create(ref, "web", { metadata: { name: "draft" }, spec: { password: "keep-private" } }).catch(() => {})}>Create</button>
      <button onClick={() => void client.create(ref, "web", { metadata: { name: "other" } }).catch(() => {})}>Create another</button>
      <button onClick={() => void client.mergePatch(ref, "web", "disk", { spec: { sizeGi: 12 } }).catch(() => {})}>Resize</button>
      <button onClick={() => void client.remove(ref, "web", "disk").catch(() => {})}>Delete</button>
      <output aria-label="pending">{writes.map((write) => write.kind).join(",")}</output>
      <output aria-label="web">{JSON.stringify(current.data?.items ?? [])}</output>
      {scoped ? <BackupList /> : null}
    </>
  );
}

function BackupList() {
  const current = useLiveList(ref, { namespace: "backup", optimisticCreates: true });
  return <output aria-label="backup">{JSON.stringify(current.data?.items ?? [])}</output>;
}

const show = (name: string) => screen.getByLabelText(name).textContent;
const click = (name: string) => fireEvent.click(screen.getByRole("button", { name }));

describe("optimistic resource writes", () => {
  it("adds a scoped placeholder immediately without copying spec or duplicating a watch result", async () => {
    const write = deferred();
    const view = renderPanel(<Fixture scoped />, { api: {
      [`GET ${path}`]: { body: list(baseline(8)) },
      [`GET ${path.replace("/web/", "/backup/")}`]: { body: list() },
      [`POST ${path}`]: () => write.result,
    } });
    await waitFor(() => expect(show("web")).toContain("disk"));
    click("Create");
    expect(show("web")).toContain("draft");
    expect(show("web")).not.toContain("password");
    expect(show("backup")).not.toContain("draft");
    expect(show("pending")).toBe("create");
    act(() => {
      view.queryClient.setQueryData(listKey(ref, "web", {}), list(baseline(8), { metadata: { name: "draft", namespace: "web", uid: "observed" } }));
    });
    expect(show("web")?.match(/"name":"draft"/g)).toHaveLength(1);
    await act(async () => { write.finish({ status: 409 }); });
    await waitFor(() => expect(show("pending")).toBe(""));
    expect(show("web")).toContain("observed");
  });

  it("projects a safe desired field and rolls back to the latest server data on rejection", async () => {
    const write = deferred();
    const view = renderPanel(<Fixture />, { api: {
      [`GET ${path}`]: { body: list(baseline(8)) },
      [`PATCH ${path}/disk`]: () => write.result,
    } });
    await waitFor(() => expect(show("web")).toContain('"sizeGi":8'));
    click("Resize");
    click("Resize");
    expect(show("web")).toContain('"sizeGi":12');
    expect(show("web")).toContain('"phase":"Bound"');
    expect(view.api.calls.filter((call) => call.method === "PATCH")).toHaveLength(1);
    act(() => { view.queryClient.setQueryData(listKey(ref, "web", {}), list(baseline(9))); });
    await act(async () => { write.finish({ status: 403 }); });
    await waitFor(() => expect(show("web")).toContain('"sizeGi":9'));
    expect(show("pending")).toBe("");
  });

  it("does not project a pending edit onto a replacement with the same name", async () => {
    const write = deferred();
    const view = renderPanel(<Fixture />, { api: {
      [`GET ${path}`]: { body: list(baseline(8)) },
      [`PATCH ${path}/disk`]: () => write.result,
    } });
    await waitFor(() => expect(show("web")).toContain('"sizeGi":8'));
    click("Resize");
    expect(show("web")).toContain('"sizeGi":12');
    act(() => {
      const object = baseline(10);
      object.metadata.uid = "replacement";
      view.queryClient.setQueryData(listKey(ref, "web", {}), list(object));
    });
    await waitFor(() => expect(show("web")).toContain('"sizeGi":10'));
    expect(show("web")).not.toContain('"sizeGi":12');
    await act(async () => { write.finish({ status: 409 }); });
  });

  it("does not cancel reconciliation for the first accepted write when a second write is accepted", async () => {
    const first = deferred();
    const second = deferred();
    let reads = 0;
    const a = { metadata: { name: "draft", namespace: "web", uid: "a" } };
    const b = { metadata: { name: "other", namespace: "web", uid: "b" } };
    renderPanel(<Fixture />, { api: {
      [`GET ${path}`]: () => [() => ({ body: list(baseline(8)) }), () => first.result, () => second.result][reads++]?.() ?? { body: list(a, b) },
      [`POST ${path}`]: { status: 201, body: a },
    } });
    await waitFor(() => expect(show("web")).toContain("disk"));
    click("Create");
    click("Create another");
    await waitFor(() => expect(reads).toBe(2));
    expect(show("pending")).toBe("create,create");
    await act(async () => { first.finish({ body: list(a) }); });
    await waitFor(() => expect(reads).toBe(3));
    expect(show("pending")).toBe("create");
    expect(show("web")).toContain('"name":"other"');
    await act(async () => { second.finish({ body: list(a, b) }); });
    await waitFor(() => expect(show("pending")).toBe(""));
    expect(show("web")).toContain('"uid":"b"');
  });

  it("keeps a foreground delete visible until the server list confirms it", async () => {
    const write = deferred();
    const reconcile = deferred();
    let accepted = false;
    renderPanel(<Fixture />, { api: {
      [`GET ${path}`]: () => accepted ? reconcile.result : { body: list(baseline(8)) },
      [`DELETE ${path}/disk`]: () => write.result,
    } });
    await waitFor(() => expect(show("web")).toContain("disk"));
    click("Delete");
    expect(show("pending")).toBe("delete");
    expect(show("web")).toContain("disk");
    await act(async () => { accepted = true; write.finish({ status: 204 }); });
    expect(show("web")).toContain("disk");
    await act(async () => { reconcile.finish({ body: list() }); });
    await waitFor(() => expect(show("web")).toBe("[]"));
    expect(show("pending")).toBe("");
  });

  it("clears local intent on session change and ignores late responses", async () => {
    const write = deferred();
    const view = renderPanel(<Fixture />, { api: {
      [`GET ${path}`]: { body: list(baseline(8)) },
      [`POST ${path}`]: () => write.result,
    } });
    await waitFor(() => expect(show("web")).toContain("disk"));
    click("Create");
    expect(show("pending")).toBe("create");
    act(() => { view.queryClient.setQueryData(sessionKey, { ...defaultSession, csrf: "new-session" }); });
    expect(show("pending")).toBe("");
    const previous = view.api.calls.filter((call) => call.method === "GET").length;
    await act(async () => { write.finish({ body: { metadata: { name: "draft", namespace: "web" } } }); });
    expect(show("pending")).toBe("");
    expect(view.api.calls.filter((call) => call.method === "GET")).toHaveLength(previous);
  });
});
