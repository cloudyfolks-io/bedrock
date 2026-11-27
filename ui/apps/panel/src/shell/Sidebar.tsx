import { useId, useRef, useState, type MouseEventHandler } from "react";
import { matchPath, NavLink, useLocation } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useSession } from "../api/session";
import { navigationFor, type NavSection } from "./navigation";

const groupIcons: Record<string, string> = {
  infrastructure: "M3 5h18v6H3zM3 13h18v6H3zM7 8h.01M7 16h.01",
  platform: "m12 3 9 5-9 5-9-5 9-5Zm9 9-9 5-9-5m18 4-9 5-9-5",
  tenancy: "M16 21v-2a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v2m18 0v-2a4 4 0 0 0-3-4M9.5 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Zm7-8a4 4 0 0 1 0 8",
  catalog: "M12 6c-2-2-5-2-9-2v15c4 0 7 0 9 2 2-2 5-2 9-2V4c-4 0-7 0-9 2Zm0 0v15",
  compute: "m8 5-5 7 5 7m3-14 5 7-5 7m5-14 5 7-5 7",
  storage: "m3 13 3-7h12l3 7m-18 0h18v5H3zM7 15.5h.01M10 15.5h.01",
  networking: "M10 3h4v5h-4zM3 16h5v5H3zM16 16h5v5h-5zM12 8v4m-6.5 4v-4h13v4",
  databases: "M20 6c0 1.7-3.6 3-8 3S4 7.7 4 6s3.6-3 8-3 8 1.3 8 3ZM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  objectStorage: "M4 8h16l-2 11a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L4 8Zm3 0V7a5 5 0 0 1 10 0v1",
  registry: "m12 3 8 4.5v9L12 21l-8-4.5v-9L12 3Zm0 9 8-4.5M12 12 4 7.5M12 12v9",
};

function SidebarGroup({ section, onNavigate }: { section: NavSection; onNavigate: MouseEventHandler<HTMLAnchorElement> }) {
  const { i18n } = useLingui();
  const location = useLocation();
  const listId = useId();
  const active = section.items.some(({ to }) => matchPath({ path: to, end: false }, location.pathname));
  const [choice, setChoice] = useState<{ open: boolean; locationKey: string } | null>(null);
  const expanded = (active && choice?.locationKey !== location.key) || choice?.open === true;

  return (
    <section className="br-nav-section">
      <h3 className="br-nav-heading">
        <button
          className="scloud-nav-group-toggle"
          type="button"
          aria-expanded={expanded}
          aria-controls={listId}
          data-active={active}
          onClick={() => setChoice({ open: !expanded, locationKey: location.key })}
        >
          <svg className="scloud-nav-group-icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d={groupIcons[section.id]} />
          </svg>
          {i18n._(section.label)}
          <svg className="scloud-nav-chevron" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="m9 6 6 6-6 6" />
          </svg>
        </button>
      </h3>
      <ul className="br-nav-list" id={listId} hidden={!expanded}>
        {section.items.map((navItem) => (
          <li key={navItem.id}>
            <NavLink to={navItem.to} className="br-nav-link" onClick={onNavigate}>
              {i18n._(navItem.label)}
            </NavLink>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function Sidebar({ project }: { project: string | undefined }) {
  const { t } = useLingui();
  const admin = useSession().data?.admin === true;
  const [open, setOpen] = useState(false);
  const toggle = useRef<HTMLButtonElement>(null);
  const scopes = [
    ...(admin ? [{ key: "admin", label: t({ id: "nav.scope.admin", message: "Administration" }), sections: navigationFor("admin", "") }] : []),
    ...(project ? [{ key: "project", label: t({ id: "nav.scope.project", message: "Project resources" }), sections: navigationFor("project", project) }] : []),
  ];
  if (!scopes.length) {
    return null;
  }
  const label = t({ id: "shell.nav.label", message: "Main navigation" });
  const closeOnNavigate: MouseEventHandler<HTMLAnchorElement> = (event) => {
    if (open && event.button === 0 && !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
      setOpen(false);
      requestAnimationFrame(() => document.getElementById("content")?.focus());
    }
  };
  return (
    <div
      className="br-sidebar scloud-rail"
      data-open={open}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          setOpen(false);
          toggle.current?.focus();
        }
      }}
    >
      <div className="br-nav-toggle">
        <button ref={toggle} className="scloud-nav-toggle" type="button" aria-expanded={open} aria-controls="panel-navigation" onClick={() => setOpen((value) => !value)}>
          {label}
          <svg className="scloud-nav-chevron" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="m7 10 5 5 5-5" />
          </svg>
        </button>
      </div>
      <nav id="panel-navigation" className="br-sidebar-links" aria-label={label}>
        {scopes.map(({ key, label, sections }) => (
          <div className="scloud-nav-scope" key={key} role="group" aria-labelledby={`navigation-${key}`}>
            <h2 className="scloud-nav-scope-label" id={`navigation-${key}`}>{label}</h2>
            {sections.map((section) => <SidebarGroup key={section.id} section={section} onNavigate={closeOnNavigate} />)}
          </div>
        ))}
      </nav>
    </div>
  );
}
