import { Suspense } from "react";
import { Outlet } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Spinner } from "../components/Spinner";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";

export function Shell() {
  const { t } = useLingui();
  return (
    <div className="br-shell">
      <a className="br-skip-link" href="#content">
        {t({ id: "shell.skip", message: "Skip to content" })}
      </a>
      <header className="br-topbar-wrap">
        <Topbar />
      </header>
      <Sidebar />
      <main id="content" className="br-content" tabIndex={-1}>
        <Suspense fallback={<Spinner />}>
          <Outlet />
        </Suspense>
      </main>
    </div>
  );
}
