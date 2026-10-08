import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Menu } from "@base-ui/react/menu";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useHttp } from "../api/HttpProvider";
import { useSession } from "../api/session";
import { useLocationActions } from "../location";

function useCaAvailable(enabled: boolean): boolean {
  const http = useHttp();
  const query = useQuery({
    queryKey: ["ca-available"],
    enabled,
    staleTime: 5 * 60 * 1000,
    queryFn: () =>
      http
        .request("HEAD", "/ca.crt")
        .then(() => true)
        .catch(() => false),
  });
  return query.data === true;
}

export function UserMenu() {
  const { t } = useLingui();
  const http = useHttp();
  const { assign } = useLocationActions();
  const session = useSession().data;
  const [open, setOpen] = useState(false);
  const caAvailable = useCaAvailable(open);

  async function signOut() {
    await http.request("POST", "/oauth/logout").catch(() => undefined);
    assign("/");
  }

  return (
    <Menu.Root open={open} onOpenChange={setOpen}>
      <Menu.Trigger className="br-button br-button--secondary" aria-label={t({ id: "shell.userMenu.label", message: "User menu" })}>
        {session?.user.displayName || session?.user.name}
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner>
          <Menu.Popup className="br-menu-popup">
            <Menu.LinkItem className="br-menu-item" render={<Link to="/account" />}>
              {t({ id: "shell.userMenu.account", message: "Account" })}
            </Menu.LinkItem>
            <Menu.LinkItem className="br-menu-item" href="/kubeconfig" download>
              {t({ id: "shell.userMenu.kubeconfig", message: "Download kubeconfig" })}
            </Menu.LinkItem>
            {caAvailable ? (
              <Menu.LinkItem className="br-menu-item" href="/ca.crt" download>
                {t({ id: "shell.userMenu.ca", message: "Download CA certificate" })}
              </Menu.LinkItem>
            ) : null}
            <Menu.Item className="br-menu-item" onClick={() => void signOut()}>
              {t({ id: "shell.userMenu.signOut", message: "Sign out" })}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
