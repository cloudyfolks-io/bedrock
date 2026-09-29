export type Route = "flow" | "device" | "account";

export function routeFor(pathname: string): Route {
  if (pathname === "/login/device") {
    return "device";
  }
  if (pathname === "/login/account") {
    return "account";
  }
  return "flow";
}

export function authRequestFromLocation(search: string): string | null {
  return new URLSearchParams(search).get("authRequest");
}

export function loginPath(): string {
  return "/login/";
}
