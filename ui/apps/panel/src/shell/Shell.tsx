import { Suspense, useEffect, useState } from "react";
import { Outlet, useMatch, useNavigate } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useProjects } from "../api/projects";
import { Spinner } from "../components/Spinner";
import { readStored, writeStored } from "../storage";
import { useRememberScope } from "./context";
import { navigationFor } from "./navigation";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";
import "../scloud-shell.css";

export function Shell() {
  const { t } = useLingui();
  const projects = useProjects().data ?? [];
  const route = useMatch("/p/:project/*");
  const navigate = useNavigate();
  const [preferred, setPreferred] = useState(() => readStored("project"));
  const active = projects.find(({ name }) => name === route?.params.project);
  const project = active ?? projects.find(({ name }) => name === preferred) ?? projects[0];
  useRememberScope();

  useEffect(() => {
    if (active?.namespace) {
      setPreferred(active.name);
    }
  }, [active]);

  function selectProject(name: string) {
    if (route) {
      const base = `/p/${name}`;
      const collection = route.params["*"]?.split("/")[0];
      const target = `${base}/${collection}`;
      const canKeepCollection = navigationFor("project", name).some(({ items }) => items.some(({ to }) => to === target));
      void navigate(canKeepCollection ? target : `${base}/vms`);
    } else {
      setPreferred(name);
      writeStored("project", name);
    }
  }

  return (
    <div className="br-shell scloud-shell">
      <a className="br-skip-link" href="#content">
        {t({ id: "shell.skip", message: "Skip to content" })}
      </a>
      <header className="br-topbar-wrap">
        <Topbar projects={projects} project={project?.name} onProjectChange={selectProject} />
      </header>
      <Sidebar project={project?.name} />
      <main id="content" className="br-content" tabIndex={-1}>
        <Suspense fallback={<Spinner />}>
          <Outlet />
        </Suspense>
      </main>
    </div>
  );
}
