import { useNavigate } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import { useProjects } from "../api/projects";
import { writeStored } from "../storage";
import { useCurrentProjectName } from "./context";

export function ProjectPicker() {
  const { t } = useLingui();
  const navigate = useNavigate();
  const projects = useProjects().data ?? [];
  const current = useCurrentProjectName() ?? "";
  if (projects.length === 0) {
    return null;
  }
  return (
    <Select
      label={t({ id: "shell.project.label", message: "Project" })}
      value={projects.some((project) => project.name === current) ? current : ""}
      options={projects.map((project) => ({ value: project.name, label: project.displayName || project.name }))}
      onValueChange={(name) => {
        writeStored("project", name);
        void navigate(`/p/${name}/vms`);
      }}
    />
  );
}
