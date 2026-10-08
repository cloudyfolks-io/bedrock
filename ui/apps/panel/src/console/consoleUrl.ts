import type { KubeObject } from "../api/types";
import { textAt } from "../resources/path";

const allowedPrefix = "/apis/bedrock.cloudyfolks.io/";

export function consoleUrl(vm: KubeObject, kind: "vnc" | "serial", location: Pick<Location, "protocol" | "host"> = window.location): string | undefined {
  const path = textAt(vm, kind === "vnc" ? "status.console.vncPath" : "status.console.serialPath");
  if (!path.startsWith(allowedPrefix) || path.includes("..") || path.includes("://") || path.includes("?") || path.includes("#") || path.includes("\\")) {
    return undefined;
  }
  return `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${path}`;
}
