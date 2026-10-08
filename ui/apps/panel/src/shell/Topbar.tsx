import { useLingui } from "@lingui/react/macro";
import { PerspectiveSwitch } from "./PerspectiveSwitch";
import { ProjectPicker } from "./ProjectPicker";
import { LanguageSelect, ThemeSelect } from "./Preferences";
import { usePerspective } from "./context";
import { UserMenu } from "./UserMenu";

export function Topbar() {
  const { t } = useLingui();
  const { perspective } = usePerspective();
  return (
    <div className="br-topbar">
      <span className="br-brand">{t({ id: "app.title", message: "Bedrock" })}</span>
      <PerspectiveSwitch />
      {perspective === "project" ? <ProjectPicker /> : null}
      <div className="br-topbar-end">
        <LanguageSelect />
        <ThemeSelect />
        <UserMenu />
      </div>
    </div>
  );
}
