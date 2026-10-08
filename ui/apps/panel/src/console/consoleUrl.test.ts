import { describe, expect, it } from "vitest";
import { serialUrl, vncUrl } from "./consoleUrl";

const vm = (vncPath: string, serialPath = "") => ({ metadata: { name: "web-1" }, status: { console: { vncPath, serialPath } } });
const https = { protocol: "https:", host: "console.lab.example" };

describe("vncUrl and serialUrl", () => {
  it("turns a proxy path into a secure WebSocket URL", () => {
    const path = "/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/web-ns/virtualmachines/web-1/console/vnc";
    expect(vncUrl(vm(path), https)).toBe(`wss://console.lab.example${path}`);
    expect(vncUrl(vm(path), { protocol: "http:", host: "localhost:8080" })).toBe(`ws://localhost:8080${path}`);
  });

  it("reads the serial path", () => {
    expect(serialUrl(vm("", "/apis/bedrock.cloudyfolks.io/v1alpha1/x/serial"), https)).toBe("wss://console.lab.example/apis/bedrock.cloudyfolks.io/v1alpha1/x/serial");
  });

  it("refuses empty, foreign and tricky paths", () => {
    for (const bad of ["", "/other/path", "https://evil.test/apis/bedrock.cloudyfolks.io/x", "/apis/bedrock.cloudyfolks.io/../etc", "/apis/bedrock.cloudyfolks.io/x?token=abc", "//evil.test/apis/bedrock.cloudyfolks.io/"]) {
      expect(vncUrl(vm(bad), https), bad).toBeUndefined();
    }
  });
});
