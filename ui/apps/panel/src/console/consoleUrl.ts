import type { KubeObject } from "../api/types";
import { textAt } from "../resources/path";

const allowedPrefix = "/apis/bedrock.cloudyfolks.io/";

type Place = Pick<Location, "protocol" | "host">;

function socketUrl(path: string, location: Place): string | undefined {
  if (!path.startsWith(allowedPrefix) || path.includes("..") || path.includes("://") || path.includes("?") || path.includes("#") || path.includes("\\")) {
    return undefined;
  }
  return `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${path}`;
}

export function vncUrl(vm: KubeObject, location: Place = window.location): string | undefined {
  return socketUrl(textAt(vm, "status.console.vncPath"), location);
}

export function serialUrl(vm: KubeObject, location: Place = window.location): string | undefined {
  return socketUrl(textAt(vm, "status.console.serialPath"), location);
}
