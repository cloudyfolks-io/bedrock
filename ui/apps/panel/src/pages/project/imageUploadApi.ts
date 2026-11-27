import type { Http } from "../../api/http";

export interface UploadGrant {
  url: string;
  token: string;
  expiresAt: string;
}

export function isUploadUrl(url: string, host: string): boolean {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" && parsed.hostname === `upload.${host}` && parsed.port === "" && parsed.username === "" && parsed.password === "";
  } catch {
    return false;
  }
}

export function requestUploadToken(http: Http, namespace: string, name: string, signal: AbortSignal): Promise<UploadGrant> {
  return http.request<UploadGrant>("POST", `/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/${encodeURIComponent(namespace)}/images/${encodeURIComponent(name)}/upload`, { body: {}, signal });
}

export function uploadFile(url: string, token: string, file: File, onProgress: (loaded: number, total: number) => void, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    signal.throwIfAborted();
    const request = new XMLHttpRequest();
    request.open("POST", url);
    request.setRequestHeader("Authorization", `Bearer ${token}`);
    request.withCredentials = false;
    request.upload.onprogress = (event) => onProgress(event.loaded, event.total);
    const abort = () => request.abort();
    const cleanup = () => signal.removeEventListener("abort", abort);
    request.onload = () => {
      cleanup();
      if (request.status >= 200 && request.status < 300) resolve();
      else reject(new Error(`upload failed with status ${request.status}`));
    };
    request.onerror = () => {
      cleanup();
      reject(new Error("upload failed"));
    };
    request.onabort = () => {
      cleanup();
      reject(new DOMException("upload aborted", "AbortError"));
    };
    signal.addEventListener("abort", abort, { once: true });
    request.send(file);
  });
}
