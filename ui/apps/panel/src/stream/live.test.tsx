import { act, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { KubeObject } from "../api/types";
import type { ResourceRef } from "../api/kube";
import { applyEvent, useLiveList, useLiveObject } from "./live";
import { SessionGate } from "../api/session";
import { createFakeSockets, type FakeSocket } from "../test/fakeSocket";
import { defaultSession, renderPanel } from "../test/renderPanel";

const vms: ResourceRef = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", plural: "virtualmachines", namespaced: true };
const listPath = "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines";
const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function vm(name: string, phase = "Running", uid = `uid-${name}`): KubeObject {
  return { metadata: { name, namespace: "web", uid, resourceVersion: "1" }, status: { phase } };
}

function List({ namespace }: { namespace: string }) {
  const query = useLiveList(vms, { namespace });
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
      api: { "GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web/virtualmachines/web-1": { body: { ...vm("web-1"), metadata: { ...vm("web-1").metadata, resourceVersion: "50" } } } },
    });
    await waitFor(() => expect(screen.getByTestId("state").textContent).toBe("web-1"));
    const socket = await openStream(sockets);
    await waitFor(() => expect(socket.framesOf("subscribe")).toHaveLength(1));
    expect(socket.framesOf("subscribe")[0]).toMatchObject({ fieldSelector: "metadata.name=web-1", resourceVersion: "50" });
    act(() => socket.receive({ type: "event", id: socket.framesOf("subscribe")[0]?.id, event: "DELETED", resourceVersion: "51", object: vm("web-1") }));
    await waitFor(() => expect(screen.getByTestId("state").textContent).toBe("deleted"));
  });
});
