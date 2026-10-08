import { NavLink } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { useSession } from "../api/session";

export function PerspectiveSwitch() {
  const { t } = useLingui();
  const admin = useSession().data?.admin === true;
  if (!admin) {
    return null;
  }
  return (
    <nav className="br-perspective" aria-label={t({ id: "shell.perspective.label", message: "Perspective" })}>
      <NavLink to="/admin/hosts" className="br-perspective-link">
        {t({ id: "shell.perspective.admin", message: "Admin" })}
      </NavLink>
      <NavLink to="/p" className="br-perspective-link">
        {t({ id: "shell.perspective.project", message: "Projects" })}
      </NavLink>
    </nav>
  );
}
