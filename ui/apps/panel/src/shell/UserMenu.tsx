import { useRef, useState } from "react";
import { useToast } from "@bedrock/design";
import { useQuery } from "@tanstack/react-query";
import { Menu } from "@base-ui/react/menu";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useHttp } from "../api/HttpProvider";
import { isHttpError } from "../api/http";
import { useSession } from "../api/session";
import { useLocationActions } from "../location";
import styles from "./userMenu.module.css";

const userIcon = "M16 8a4 4 0 1 1-8 0 4 4 0 0 1 8 0ZM4 21v-1a5 5 0 0 1 5-5h6a5 5 0 0 1 5 5v1";
const downloadIcon = "M12 3v12m-4-4 4 4 4-4M4 17v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3";
const signOutIcon = "M10 17v3H4V4h6v3m4 1 4 4-4 4m4-4H9";

function MenuIcon({ path }: { path: string }) {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}

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
  const toast = useToast();
  const session = useSession();
  const user = session.data?.user;
  const displayName = user?.displayName || user?.name;
  const [open, setOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const blocked = useRef(false);
  const caAvailable = useCaAvailable(open);

  async function signOut() {
    if (blocked.current) return;
    blocked.current = true;
    setSigningOut(true);
    try {
      await http.request("POST", "/oauth/logout");
      assign("/");
    } catch (error) {
      if (isHttpError(error) && error.status === 401) {
        assign("/");
      } else {
        toast.show({ tone: "danger", title: t({ id: "shell.userMenu.signOutFailed", message: "Could not sign out" }) });
        // The proxy can clear the cookie before reporting a logout error.
        await session.refetch();
      }
    } finally {
      blocked.current = false;
      setSigningOut(false);
    }
  }

  return (
    <Menu.Root open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen || !blocked.current) setOpen(nextOpen);
    }}>
      <Menu.Trigger className={`${styles.avatar} ${styles.trigger}`} aria-disabled={signingOut || undefined} aria-label={t({ id: "shell.userMenu.label", message: "User menu" })}>
        <MenuIcon path={userIcon} />
      </Menu.Trigger>
      <span className="br-visually-hidden" aria-live="polite">
        {signingOut ? t({ id: "shell.userMenu.signingOut", message: "Signing out…" }) : null}
      </span>
      <Menu.Portal>
        <Menu.Positioner className={styles.positioner} align="end" sideOffset={8} positionMethod="fixed" collisionPadding={8} collisionAvoidance={{ align: "shift", fallbackAxisSide: "none" }}>
          <Menu.Popup className={styles.popup}>
            <Menu.Group>
              {displayName ? (
                <Menu.GroupLabel className={styles.identity}>
                  <span className={styles.avatar}><MenuIcon path={userIcon} /></span>
                  <bdi>{displayName}</bdi>
                </Menu.GroupLabel>
              ) : null}
              <Menu.LinkItem className={styles.item} render={<Link to="/account" />}>
                <MenuIcon path={userIcon} />
                {t({ id: "shell.userMenu.account", message: "Account" })}
              </Menu.LinkItem>
              <Menu.LinkItem className={styles.item} href="/kubeconfig" download>
                <MenuIcon path={downloadIcon} />
                {t({ id: "shell.userMenu.kubeconfig", message: "Download kubeconfig" })}
              </Menu.LinkItem>
              {caAvailable ? (
                <Menu.LinkItem className={styles.item} href="/ca.crt" download>
                  <MenuIcon path={downloadIcon} />
                  {t({ id: "shell.userMenu.ca", message: "Download CA certificate" })}
                </Menu.LinkItem>
              ) : null}
            </Menu.Group>
            <Menu.Separator className={styles.separator} />
            <Menu.Item className={`${styles.item} ${styles.signOut}`} onClick={() => void signOut()}>
              <MenuIcon path={signOutIcon} />
              {t({ id: "shell.userMenu.signOut", message: "Sign out" })}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
