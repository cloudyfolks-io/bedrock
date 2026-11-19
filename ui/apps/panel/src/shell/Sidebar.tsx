import { useState } from "react";
import { NavLink } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useSession } from "../api/session";
import { navigationFor } from "./navigation";
import { useCurrentProjectName, usePerspective } from "./context";

export function Sidebar() {
  const { t, i18n } = useLingui();
  const { perspective } = usePerspective();
  const admin = useSession().data?.admin === true;
  const project = useCurrentProjectName();
  const [open, setOpen] = useState(false);
  if (perspective === "admin" && !admin) {
    return null;
  }
  const sections = perspective === "project" && !project ? [] : navigationFor(perspective, project ?? "");
  const label = t({ id: "shell.nav.label", message: "Main navigation" });
  return (
    <div className="br-sidebar" data-open={open}>
      {sections.length ? (
        <div className="br-nav-toggle">
          <button className="br-button br-button--secondary" type="button" aria-expanded={open} aria-controls="panel-navigation" onClick={() => setOpen((value) => !value)}>
            {label}
          </button>
        </div>
      ) : null}
      <nav id="panel-navigation" className="br-sidebar-links" aria-label={label}>
        {sections.map((section) => (
          <section key={section.id} className="br-nav-section">
            <h2 className="br-nav-heading">{i18n._(section.label)}</h2>
            <ul className="br-nav-list">
              {section.items.map((navItem) => (
                <li key={navItem.id}>
                  <NavLink to={navItem.to} className="br-nav-link" onClick={(event) => {
                    if (open && event.button === 0 && !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
                      setOpen(false);
                      requestAnimationFrame(() => document.getElementById("content")?.focus());
                    }
                  }}>
                    {i18n._(navItem.label)}
                  </NavLink>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </nav>
    </div>
  );
}
