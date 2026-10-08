import { NavLink } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { navigationFor } from "./navigation";
import { useCurrentProjectName, usePerspective } from "./context";

export function Sidebar() {
  const { t, i18n } = useLingui();
  const { perspective } = usePerspective();
  const project = useCurrentProjectName();
  const sections = perspective === "project" && !project ? [] : navigationFor(perspective, project ?? "");
  return (
    <nav className="br-sidebar" aria-label={t({ id: "shell.nav.label", message: "Main navigation" })}>
      {sections.map((section) => (
        <section key={section.id} className="br-nav-section">
          <h2 className="br-nav-heading">{i18n._(section.label)}</h2>
          <ul className="br-nav-list">
            {section.items.map((navItem) => (
              <li key={navItem.id}>
                <NavLink to={navItem.to} className="br-nav-link">
                  {i18n._(navItem.label)}
                </NavLink>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </nav>
  );
}
