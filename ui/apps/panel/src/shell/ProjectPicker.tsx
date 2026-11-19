import { useNavigate } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Select } from "@bedrock/design";
import { projectOptions, useProjects } from "../api/projects";
import { writeStored } from "../storage";
import { useCurrentProjectName } from "./context";

export function ProjectPicker() {
  const { t } = useLingui();
  const navigate = useNavigate();
  const query = useProjects();
  const projects = query.isSuccess ? query.data : [];
  const current = useCurrentProjectName() ?? "";
  if (projects.length === 0) {
    return null;
  }
  return (
    <Select
      label={t({ id: "shell.project.label", message: "Project" })}
      value={projects.some((project) => project.name === current) ? current : ""}
      options={projectOptions(projects)}
      onValueChange={(name) => {
        writeStored("project", name);
        void navigate(`/p/${name}/vms`);
      }}
    />
  );
}
