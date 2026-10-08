import { beforeEach, describe, expect, it, vi } from "vitest";
import { createHttp } from "../../api/http";
import { FakeXhr } from "../../test/fakeXhr";
import { isUploadUrl, requestUploadToken, uploadFile } from "./imageUpload";

describe("isUploadUrl", () => {
  it("accepts only https on upload.<host> without port or credentials", () => {
    expect(isUploadUrl("https://upload.lab.test/v1beta1/upload-async", "lab.test")).toBe(true);
    for (const bad of ["https://lab.test/x", "https://upload.lab.test.evil/x", "http://upload.lab.test/x", "https://upload.lab.test:444/x", "https://a:b@upload.lab.test/x", "https://xupload.lab.test/x", "not a url"]) {
      expect(isUploadUrl(bad, "lab.test"), bad).toBe(false);
    }
  });
});

describe("TestNoTokenInUrls", () => {
  beforeEach(() => FakeXhr.install());

  it("keeps the CSRF value and the grant token out of every URL", async () => {
    const csrf = "csrf-secret-value-0123456789";
    const grant = { url: "https://upload.lab.test/v1beta1/upload-async", token: "grant-secret-token", expiresAt: "2026-10-09T00:00:00Z" };
    const fetchMock = vi.fn(() => Promise.resolve(new Response(JSON.stringify(grant), { status: 200 })));
    const http = createHttp({ fetch: fetchMock as unknown as typeof fetch, csrf: () => csrf, onUnauthenticated: () => {} });
    const received = await requestUploadToken(http, "web-ns", "installer");
    const pending = uploadFile(received.url, received.token, new File(["x"], "a.iso"), () => {}, new AbortController().signal);
    FakeXhr.latest().finish(200);
    await pending;
    const urls = [...fetchMock.mock.calls.map((call) => String((call as unknown[])[0])), ...FakeXhr.instances.map((xhr) => xhr.url)];
    expect(urls).toHaveLength(2);
    for (const url of urls) {
      expect(url).not.toContain(csrf);
      expect(url).not.toContain(grant.token);
    }
    expect(FakeXhr.latest().headers.Authorization).toBe(`Bearer ${grant.token}`);
  });
});
