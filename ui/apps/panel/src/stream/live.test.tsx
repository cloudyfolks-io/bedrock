import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { KubeObject } from "../api/types";
import { kubeClient, objectKey, type ResourceRef } from "../api/kube";
import { applyEvent, useLiveList, useLiveObject } from "./live";
import { sessionKey, useHttp } from "../api/HttpProvider";
import { SessionGate } from "../api/session";
import type { FakeReply } from "../test/fakeApi";
import { createFakeSockets, type FakeSocket } from "../test/fakeSocket";
import { defaultSession, renderPanel } from "../test/renderPanel";

const vms: ResourceRef = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", plural: "virtualmachines", namespaced: true };
const listPath = "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines";
const objectPath = `${listPath}/web-1`;
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function vm(name: string, phase = "Running", uid = `uid-${name}`): KubeObject {
  return { metadata: { name, namespace: "web", uid, resourceVersion: "1" }, status: { phase } };
}

function List({ namespace, labelSelector }: { namespace: string; labelSelector?: string }) {
  const query = useLiveList(vms, { namespace, ...(labelSelector === undefined ? {} : { labelSelector }) });
  return (
    <>
      <p data-testid="live">{query.live}</p>
      <ul>
        {query.data?.items.map((item) => (
          <li key={item.metadata.uid}>{`${item.metadata.name}:${String(item.status?.phase)}`}</li>
        ))}
      </ul>
    </>
  );
}

function ObjectPhase() {
  const query = useLiveObject(vms, "web", "web-1");
  return <p>{query.data === null ? "deleted" : String(query.data?.status?.phase ?? "loading")}</p>;
}

function deferredReply() {
  let resolve!: (reply: FakeReply) => void;
  const reply = new Promise<FakeReply>((finish) => { resolve = finish; });
  return { reply, resolve };
}

async function openStream(sockets: ReturnType<typeof createFakeSockets>): Promise<FakeSocket> {
  await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
  const socket = sockets.latest();
  act(() => {
    socket.open();
    socket.receive(hello);
  });
  return socket;
}

function setup(namespace = "web", listBody: () => unknown = () => ({ items: [vm("web-1")], metadata: { resourceVersion: "100" } })) {
  const sockets = createFakeSockets();
  let lists = 0;
  const view = renderPanel(<List namespace={namespace} />, {
    socket: sockets,
    api: {
      [listPath]: () => {
        lists += 1;
        return { body: listBody() };
      },
    },
  });
  return { sockets, view, lists: () => lists };
}

describe("applyEvent", () => {
  it("TestApplyEvent", () => {
    const base = [vm("a"), vm("b")];
    const frozen = Object.freeze([...base]);
    const added = applyEvent(frozen, { event: "ADDED", object: vm("c") });
    expect(added.map((item) => item.metadata.name)).toEqual(["a", "b", "c"]);
    expect(frozen).toHaveLength(2);
    const modified = applyEvent(added, { event: "MODIFIED", object: vm("b", "Stopped") });
    expect(modified.find((item) => item.metadata.name === "b")?.status?.phase).toBe("Stopped");
    expect(applyEvent(modified, { event: "DELETED", object: vm("a") }).map((item) => item.metadata.name)).toEqual(["b", "c"]);
    expect(applyEvent(base, { event: "DELETED", object: vm("zzz") })).toEqual(base);
    const once = applyEvent(base, { event: "ADDED", object: vm("n") });
    expect(applyEvent(once, { event: "ADDED", object: vm("n") })).toEqual(once);
  });
});

describe("useLiveList", () => {
  it("TestListThenSubscribe", async () => {
    const { sockets, lists } = setup();
    expect(await screen.findByText("web-1:Running")).toBeTruthy();
    expect(screen.getByTestId("live").textContent).toBe("connecting");
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    expect(socket.framesOf("subscribe")[0]).toMatchObject({ resource: "virtualmachines", namespace: "web", resourceVersion: "100" });
    act(() => socket.receive({ type: "subscribed", id: socket.framesOf("subscribe")[0]?.id }));
    await waitFor(() => expect(screen.getByTestId("live").textContent).toBe("live"));
    expect(lists()).toBe(1);
  });

  it("TestEventsPatchTheCache", async () => {
    const { sockets, lists } = setup();
    await screen.findByText("web-1:Running");
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    const id = socket.framesOf("subscribe")[0]?.id;
    act(() => socket.receive({ type: "event", id, event: "ADDED", resourceVersion: "101", object: vm("web-2") }));
    expect(await screen.findByText("web-2:Running")).toBeTruthy();
    act(() => socket.receive({ type: "event", id, event: "MODIFIED", resourceVersion: "102", object: vm("web-2", "Stopped") }));
    expect(await screen.findByText("web-2:Stopped")).toBeTruthy();
    act(() => socket.receive({ type: "event", id, event: "DELETED", resourceVersion: "103", object: vm("web-1") }));
    await waitFor(() => expect(screen.queryByText("web-1:Running")).toBeNull());
    expect(lists()).toBe(1);
  });

  it("restarts a shared filtered watch from a write refetch so newer events replay", async () => {
    const snapshot = deferredReply();
    let lists = 0;
    function Write() {
      const client = kubeClient(useHttp());
      return <button onClick={() => void client.mergePatch(vms, "web", "web-1", { spec: { enabled: false } })}>Update</button>;
    }
    const selector = "app=web";
    const { sockets } = renderPanel(
      <>
        <List namespace="web" labelSelector={selector} />
        <List namespace="web" labelSelector={selector} />
        <Write />
      </>,
      { api: {
        [listPath]: ({ query }) => {
          expect(query.get("labelSelector")).toBe(selector);
          lists += 1;
          return lists === 1 ? { body: { items: [vm("web-1")], metadata: { resourceVersion: "initial" } } } : snapshot.reply;
        },
        [objectPath.replace("GET", "PATCH")]: { body: vm("web-1") },
      } },
    );
    expect(await screen.findAllByText("web-1:Running")).toHaveLength(2);
    const socket = await openStream(sockets);
    const [oldWatch] = socket.framesOf("subscribe");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));
    await waitFor(() => expect(lists).toBe(2));
    const event = { type: "event", event: "MODIFIED", resourceVersion: "a-newer-event", object: vm("web-1", "Stopped") };
    act(() => socket.receive({ ...event, id: oldWatch?.id }));
    expect(await screen.findAllByText("web-1:Stopped")).toHaveLength(2);

    await act(async () => snapshot.resolve({ body: { items: [vm("web-1")], metadata: { resourceVersion: "z-snapshot" } } }));
    expect(socket.framesOf("unsubscribe")).toEqual([{ op: "unsubscribe", id: oldWatch?.id }]);
    const [, newWatch] = socket.framesOf("subscribe");
    expect(socket.framesOf("subscribe")).toHaveLength(2);
    expect(newWatch).toMatchObject({ namespace: "web", labelSelector: selector, resourceVersion: "z-snapshot" });
    expect(newWatch?.id).not.toBe(oldWatch?.id);
    act(() => socket.receive({ ...event, id: newWatch?.id }));
    expect(await screen.findAllByText("web-1:Stopped")).toHaveLength(2);
  });

  it("TestRelistRefetchesAndResubscribes", async () => {
    let version = 100;
    const { sockets, lists } = setup("web", () => {
      version += 100;
      return { items: [vm("web-1")], metadata: { resourceVersion: String(version) } };
    });
    await screen.findByText("web-1:Running");
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    act(() => socket.receive({ type: "relist", id: socket.framesOf("subscribe")[0]?.id }));
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(2));
    expect(lists()).toBe(2);
    expect(socket.framesOf("subscribe")[0]?.resourceVersion).toBe("200");
    expect(socket.framesOf("subscribe")[1]?.resourceVersion).toBe("300");
  });

  it("TestTwoComponentsShareOneSubscription", async () => {
    const sockets = createFakeSockets();
    const both = (
      <>
        <List namespace="web" />
        <List namespace="web" />
      </>
    );
    const view = renderPanel(both, { socket: sockets, api: { [listPath]: { body: { items: [vm("web-1")], metadata: { resourceVersion: "100" } } } } });
    await waitFor(() => expect(screen.getAllByText("web-1:Running")).toHaveLength(2));
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    view.rerender(<p>gone</p>);
    await waitFor(() => expect(socket.framesOf("unsubscribe")).toHaveLength(1));
  });

  it("TestScopeChangeMovesTheSubscription", async () => {
    const sockets = createFakeSockets();
    const view = renderPanel(<List namespace="web" />, {
      socket: sockets,
      api: {
        [listPath]: { body: { items: [vm("web-1")], metadata: { resourceVersion: "100" } } },
        "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/other/virtualmachines": { body: { items: [], metadata: { resourceVersion: "5" } } },
      },
    });
    await screen.findByText("web-1:Running");
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    view.rerender(<List namespace="other" />);
    await waitFor(() => expect(socket.framesOf("unsubscribe")).toHaveLength(1));
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(2));
    expect(socket.framesOf("subscribe")[1]).toMatchObject({ namespace: "other", resourceVersion: "5" });
  });

  it("TestForbiddenStreamPausesLive", async () => {
    const { sockets } = setup();
    await screen.findByText("web-1:Running");
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    act(() => socket.receive({ type: "error", id: socket.framesOf("subscribe")[0]?.id, code: "forbidden", message: "no" }));
    await waitFor(() => expect(screen.getByTestId("live").textContent).toBe("paused"));
    expect(screen.getByText("web-1:Running")).toBeTruthy();
  });

  it("TestSessionEndedClearsTheGate sends the user to sign in", async () => {
    const sockets = createFakeSockets();
    const assign = vi.fn();
    let sessionCalls = 0;
    renderPanel(
      <SessionGate>
        <List namespace="web" />
      </SessionGate>,
      {
        socket: sockets,
        session: null,
        assign,
        api: {
          "GET /proxy/v1/session": () => {
            sessionCalls += 1;
            return sessionCalls === 1 ? { body: defaultSession } : { status: 401, body: { error: "unauthenticated" } };
          },
          [listPath]: { body: { items: [vm("web-1")], metadata: { resourceVersion: "100" } } },
        },
      },
    );
    await screen.findByText("web-1:Running");
    const socket = await openStream(sockets);
    act(() => socket.close(4401));
    await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    expect(assign.mock.calls[0]?.[0]).toMatch(/^\/oauth\/login\?returnTo=/);
    expect(screen.queryByText("web-1:Running")).toBeNull();
  });
});

describe("useLiveObject", () => {
  it("TestObjectDeletedBecomesNull", async () => {
    function One() {
      const query = useLiveObject(vms, "web", "web-1");
      return <p data-testid="state">{query.data === null ? "deleted" : (query.data?.metadata.name ?? "loading")}</p>;
    }
    const sockets = createFakeSockets();
    renderPanel(<One />, {
      socket: sockets,
      api: { [objectPath]: { body: { ...vm("web-1"), metadata: { ...vm("web-1").metadata, resourceVersion: "50" } } } },
    });
    await waitFor(() => expect(screen.getByTestId("state").textContent).toBe("web-1"));
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    expect(socket.framesOf("subscribe")[0]).toMatchObject({ fieldSelector: "metadata.name=web-1", resourceVersion: "50" });
    act(() => socket.receive({ type: "event", id: socket.framesOf("subscribe")[0]?.id, event: "DELETED", resourceVersion: "51", object: vm("web-1") }));
    await waitFor(() => expect(screen.getByTestId("state").textContent).toBe("deleted"));
  });

  it("rejects old-session events and relists before React cleans up the subscription", async () => {
    const { sockets, queryClient } = renderPanel(<SessionGate><ObjectPhase /></SessionGate>, {
      api: { [objectPath]: { body: vm("web-1") } },
    });
    await screen.findByText("Running");
    const socket = await openStream(sockets);
    const [watch] = socket.framesOf("subscribe");
    const key = objectKey(vms, "web", "web-1");
    const frame = { type: "event", id: watch?.id, event: "MODIFIED", resourceVersion: "late", object: vm("web-1", "Old session") };
    act(() => {
      queryClient.setQueryData(sessionKey, { ...defaultSession, csrf: "new-session" });
      socket.receive(frame);
      expect(queryClient.getQueryData(key)).toBeUndefined();
      const replacement = vm("web-1", "New session", "new-uid");
      queryClient.setQueryData(key, replacement);
      socket.receive(frame);
      socket.receive({ type: "relist", id: watch?.id });
      expect(queryClient.getQueryData(key)).toEqual(replacement);
      expect(queryClient.getQueryState(key)?.isInvalidated).toBe(false);
    });
    expect(socket.framesOf("unsubscribe")).toContainEqual({ op: "unsubscribe", id: watch?.id });
  });

  it("uses a scoped snapshot cursor after a refresh returns 404 so a recreation can replay", async () => {
    const snapshot = deferredReply();
    let gets = 0;
    const { sockets, api, queryClient } = renderPanel(<ObjectPhase />, {
      api: {
        [objectPath]: () => ++gets === 1 ? { body: vm("web-1") } : { status: 404 },
        [listPath]: () => snapshot.reply,
      },
    });
    await screen.findByText("Running");
    const socket = await openStream(sockets);
    const [oldWatch] = socket.framesOf("subscribe");
    let refreshing: Promise<void>;
    act(() => { refreshing = queryClient.invalidateQueries({ queryKey: objectKey(vms, "web", "web-1") }); });
    await waitFor(() => expect(api.calls.some(({ query }) => query.get("fieldSelector") === "metadata.name=web-1")).toBe(true));
    const recreated = { type: "event", event: "ADDED", resourceVersion: "recreated", object: vm("web-1", "Recreated", "new-uid") };
    act(() => socket.receive({ ...recreated, id: oldWatch?.id }));
    await screen.findByText("Recreated");
    await act(async () => {
      snapshot.resolve({ body: { items: [], metadata: { resourceVersion: "empty-snapshot" } } });
      await refreshing;
    });
    const [, newWatch] = socket.framesOf("subscribe");
    expect(newWatch).toMatchObject({ namespace: "web", fieldSelector: "metadata.name=web-1", resourceVersion: "empty-snapshot" });
    act(() => socket.receive({ ...recreated, id: newWatch?.id }));
    await screen.findByText("Recreated");
  });
});
