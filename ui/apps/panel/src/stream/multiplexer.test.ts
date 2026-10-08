import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { Multiplexer } from "./multiplexer";
import type { StreamHandlers, Subscription } from "./protocol";
import { createFakeSockets, type FakeSocket, type FakeSockets } from "../test/fakeSocket";

const vmSub: Subscription = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", resource: "virtualmachines", namespace: "web" };
const volSub: Subscription = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", resource: "volumes", namespace: "web" };
const subnetSub: Subscription = { group: "bedrock.cloudyfolks.io", version: "v1alpha1", resource: "subnets", namespace: "web" };

function handlers(overrides: Partial<StreamHandlers> = {}): StreamHandlers {
  return {
    resourceVersion: () => "100",
    onEvent: vi.fn(),
    onRelist: vi.fn(),
    onError: vi.fn(),
    ...overrides,
  };
}

const hello = { type: "hello", expiresAt: "2026-10-09T00:00:00Z", limits: { maxSubscriptions: 64 } };

function connect(sockets: FakeSockets, limit = 64): FakeSocket {
  const socket = sockets.latest();
  socket.open();
  socket.receive({ ...hello, limits: { maxSubscriptions: limit } });
  return socket;
}

describe("Multiplexer", () => {
  let sockets: FakeSockets;
  let onSessionEnded: Mock<() => void>;

  function make(random = () => 1): Multiplexer {
    return new Multiplexer({ url: "wss://console.test/proxy/v1/stream", createSocket: sockets.create, random, onSessionEnded });
  }

  beforeEach(() => {
    vi.useFakeTimers();
    sockets = createFakeSockets();
    onSessionEnded = vi.fn<() => void>();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("TestConnectsLazilyWithTheSubprotocol", () => {
    const mux = make();
    expect(sockets.sockets).toHaveLength(0);
    mux.acquire(vmSub, handlers());
    expect(sockets.sockets).toHaveLength(1);
    expect(sockets.latest().protocols).toEqual(["bedrock.stream.v1"]);
    expect(sockets.latest().url).toBe("wss://console.test/proxy/v1/stream");
  });

  it("TestSubscribeSendsTheResourceVersion", () => {
    const mux = make();
    mux.acquire({ ...vmSub, labelSelector: "a=b", fieldSelector: "x=y" }, handlers({ resourceVersion: () => "12345" }));
    const socket = sockets.latest();
    socket.open();
    expect(socket.framesOf("subscribe")).toHaveLength(0);
    socket.receive(hello);
    const frame = socket.framesOf("subscribe")[0];
    expect(frame).toMatchObject({
      op: "subscribe",
      group: "bedrock.cloudyfolks.io",
      version: "v1alpha1",
      resource: "virtualmachines",
      namespace: "web",
      labelSelector: "a=b",
      fieldSelector: "x=y",
      resourceVersion: "12345",
    });
    expect(String(frame?.id)).toMatch(/^[A-Za-z0-9._-]{1,64}$/);
  });

  it("TestEventsAdvanceTheResumePoint", () => {
    const mux = make();
    mux.acquire(vmSub, handlers());
    const socket = connect(sockets);
    const id = String(socket.framesOf("subscribe")[0]?.id);
    socket.receive({ type: "event", id, event: "ADDED", resourceVersion: "12350", object: { metadata: { name: "a" } } });
    socket.receive({ type: "bookmark", id, resourceVersion: "12400" });
    socket.close(1013);
    vi.advanceTimersByTime(1000);
    const next = connect(sockets);
    expect(next.framesOf("subscribe")[0]?.resourceVersion).toBe("12400");
  });

  it("TestAcquireTwiceSendsOneSubscription", () => {
    const mux = make();
    const releaseA = mux.acquire(vmSub, handlers());
    const releaseB = mux.acquire(vmSub, handlers());
    const socket = connect(sockets);
    expect(socket.framesOf("subscribe")).toHaveLength(1);
    releaseA();
    expect(socket.framesOf("unsubscribe")).toHaveLength(0);
    releaseB();
    expect(socket.framesOf("unsubscribe")).toHaveLength(1);
    vi.advanceTimersByTime(4900);
    expect(socket.closeCodes).toHaveLength(0);
    vi.advanceTimersByTime(200);
    expect(socket.closeCodes).toHaveLength(1);
    expect(mux.status()).toBe("idle");
  });

  it("TestIdsAreUniqueAndStableAcrossReconnects", () => {
    const mux = make();
    mux.acquire(vmSub, handlers());
    mux.acquire(volSub, handlers());
    const first = connect(sockets);
    const ids = first.framesOf("subscribe").map((frame) => frame.id);
    expect(new Set(ids).size).toBe(2);
    first.close(1013);
    vi.advanceTimersByTime(1000);
    const second = connect(sockets);
    expect(second.framesOf("subscribe").map((frame) => frame.id)).toEqual(ids);
  });

  it("TestReconnectBackoff", () => {
    const mux = make(() => 1);
    mux.acquire(vmSub, handlers());
    sockets.latest().close(1006);
    const expected = [1000, 2000, 4000, 8000, 16000, 30000, 30000];
    for (const [index, delay] of expected.entries()) {
      const count = sockets.sockets.length;
      vi.advanceTimersByTime(delay - 1);
      expect(sockets.sockets.length, `attempt ${index} early`).toBe(count);
      vi.advanceTimersByTime(1);
      expect(sockets.sockets.length, `attempt ${index} on time`).toBe(count + 1);
      sockets.latest().close(1006);
    }
  });

  it("halves the delay with no jitter and resets the counter after hello", () => {
    const mux = make(() => 0);
    mux.acquire(vmSub, handlers());
    sockets.latest().close(1006);
    vi.advanceTimersByTime(500);
    expect(sockets.sockets).toHaveLength(2);
    sockets.latest().close(1006);
    vi.advanceTimersByTime(1000);
    expect(sockets.sockets).toHaveLength(3);
    connect(sockets);
    sockets.latest().close(1006);
    vi.advanceTimersByTime(500);
    expect(sockets.sockets).toHaveLength(4);
  });

  it("TestRelistFrameCallsOnRelist", () => {
    const mux = make();
    const onRelist = vi.fn();
    let version = "100";
    const release = mux.acquire(vmSub, handlers({ onRelist, resourceVersion: () => version }));
    const socket = connect(sockets);
    const id = String(socket.framesOf("subscribe")[0]?.id);
    socket.receive({ type: "relist", id });
    expect(onRelist).toHaveBeenCalledTimes(1);
    release();
    expect(socket.framesOf("unsubscribe")).toHaveLength(0);
    version = "200";
    mux.acquire(vmSub, handlers({ resourceVersion: () => version }));
    expect(socket.framesOf("subscribe")).toHaveLength(2);
    expect(socket.framesOf("subscribe")[1]?.resourceVersion).toBe("200");
  });

  it("TestErrorFrames", () => {
    const mux = make();
    const onError = vi.fn();
    mux.acquire(vmSub, handlers({ onError }));
    const socket = connect(sockets);
    const id = String(socket.framesOf("subscribe")[0]?.id);
    for (const code of ["forbidden", "resource_not_allowed", "invalid"]) {
      mux.acquire({ ...vmSub, namespace: code }, handlers({ onError }));
    }
    socket.receive({ type: "error", id, code: "forbidden", message: "no" });
    expect(onError).toHaveBeenCalledWith("forbidden", "no");
    vi.advanceTimersByTime(60000);
    expect(socket.framesOf("subscribe").filter((frame) => frame.id === id)).toHaveLength(1);
  });

  it("retries a subscription after unavailable with the backoff", () => {
    const mux = make();
    const onError = vi.fn();
    mux.acquire(vmSub, handlers({ onError }));
    const socket = connect(sockets);
    const id = String(socket.framesOf("subscribe")[0]?.id);
    socket.receive({ type: "error", id, code: "unavailable", message: "later" });
    expect(onError).not.toHaveBeenCalled();
    vi.advanceTimersByTime(999);
    expect(socket.framesOf("subscribe")).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(socket.framesOf("subscribe")).toHaveLength(2);
  });

  it("cancels a pending unavailable retry when the socket closes", () => {
    const draws = [1, 0];
    const mux = make(() => draws.shift() ?? 0);
    mux.acquire(vmSub, handlers());
    const first = connect(sockets);
    const id = String(first.framesOf("subscribe")[0]?.id);
    first.receive({ type: "error", id, code: "unavailable", message: "later" });
    first.close(1013);
    vi.advanceTimersByTime(500);
    const second = connect(sockets);
    expect(second.framesOf("subscribe")).toHaveLength(1);
    vi.advanceTimersByTime(60000);
    expect(second.framesOf("subscribe")).toHaveLength(1);
    expect(first.framesOf("subscribe")).toHaveLength(1);
  });

  it("TestSessionEndedCloseCode4401", () => {
    const mux = make();
    mux.acquire(vmSub, handlers());
    const socket = connect(sockets);
    socket.close(4401);
    expect(onSessionEnded).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(120000);
    expect(sockets.sockets).toHaveLength(1);
    expect(mux.status()).toBe("ended");
  });

  it("TestOverloadedCloseReconnects", () => {
    const mux = make();
    mux.acquire(vmSub, handlers());
    connect(sockets).close(1013);
    expect(mux.status()).toBe("reconnecting");
    vi.advanceTimersByTime(1000);
    expect(sockets.sockets).toHaveLength(2);
  });

  it("TestKeepalive", () => {
    const mux = make();
    mux.acquire(vmSub, handlers());
    const socket = connect(sockets);
    vi.advanceTimersByTime(25000);
    expect(socket.framesOf("ping")).toHaveLength(1);
    socket.receive({ type: "pong" });
    vi.advanceTimersByTime(25000);
    expect(socket.framesOf("ping")).toHaveLength(2);
    socket.receive({ type: "pong" });
    vi.advanceTimersByTime(59000);
    expect(socket.closeCodes).toHaveLength(0);
    vi.advanceTimersByTime(2000);
    expect(socket.closeCodes).toHaveLength(1);
    vi.advanceTimersByTime(1000);
    expect(sockets.sockets).toHaveLength(2);
  });

  it("TestLimitFromHello", () => {
    const mux = make();
    const onError = vi.fn();
    mux.acquire(vmSub, handlers());
    mux.acquire(volSub, handlers());
    const socket = connect(sockets, 2);
    mux.acquire(subnetSub, handlers({ onError }));
    expect(onError).toHaveBeenCalledWith("limit_exceeded", expect.any(String));
    expect(socket.framesOf("subscribe")).toHaveLength(2);
  });

  it("TestUnknownFramesAreIgnored", () => {
    const mux = make();
    const onEvent = vi.fn();
    mux.acquire(vmSub, handlers({ onEvent }));
    const socket = connect(sockets);
    socket.receive({ type: "mystery", id: "x" });
    socket.receive({ type: "event", id: "unknown", event: "ADDED", resourceVersion: "1", object: { metadata: { name: "a" } } });
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("TestFramesAfterUnsubscribeAreDropped", () => {
    const mux = make();
    const onEvent = vi.fn();
    const release = mux.acquire(vmSub, handlers({ onEvent }));
    const socket = connect(sockets);
    const id = String(socket.framesOf("subscribe")[0]?.id);
    release();
    socket.receive({ type: "event", id, event: "ADDED", resourceVersion: "1", object: { metadata: { name: "a" } } });
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("TestMalformedJsonIsIgnored", () => {
    const mux = make();
    const onEvent = vi.fn();
    mux.acquire(vmSub, handlers({ onEvent }));
    const socket = connect(sockets);
    expect(() => socket.receiveRaw("{not json")).not.toThrow();
    expect(mux.status()).toBe("live");
  });

  it("notifies the subscribed handler and status listeners", () => {
    const mux = make();
    const onSubscribed = vi.fn();
    const statuses: string[] = [];
    mux.onStatus((status) => statuses.push(status));
    mux.acquire(vmSub, handlers({ onSubscribed }));
    const socket = connect(sockets);
    socket.receive({ type: "subscribed", id: String(socket.framesOf("subscribe")[0]?.id) });
    expect(onSubscribed).toHaveBeenCalledTimes(1);
    expect(statuses).toEqual(["connecting", "live"]);
  });
});
