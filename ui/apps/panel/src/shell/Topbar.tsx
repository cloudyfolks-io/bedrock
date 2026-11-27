import { Link } from "react-router";
import type { Project } from "../api/projects";
import { ProjectPicker } from "./ProjectPicker";
import { LanguageSelect } from "./Preferences";
import { UserMenu } from "./UserMenu";

export function Topbar({ projects, project, onProjectChange }: { projects: readonly Project[]; project: string | undefined; onProjectChange: (name: string) => void }) {
  return (
    <div className="br-topbar">
      <Link className="br-brand" to="/" dir="ltr" aria-label="sCloud">
        <svg viewBox="0 0 792 686" width="23" height="20" fill="currentColor" aria-hidden="true">
          <path fillOpacity=".3" d="M396 0.11A396 396 0 0 1 540.95 145.05A396 396 0 0 1 738.95 92A396 396 0 0 1 396 686A396 396 0 0 1 53.05 92A396 396 0 0 1 251.05 145.05A396 396 0 0 1 396 0.11Z" />
          <path fillOpacity=".4286" d="M251.05 145.05A396 396 0 0 1 396 290A396 396 0 0 1 540.95 145.05A396 396 0 0 1 594 343.05A396 396 0 0 1 792 290A396 396 0 0 1 396 686A396 396 0 0 1 0 290A396 396 0 0 1 198 343.05A396 396 0 0 1 251.05 145.05Z" />
          <path d="M396 290A396 396 0 0 1 449.05 488A396 396 0 0 1 594 343.05A396 396 0 0 1 396 686A396 396 0 0 1 198 343.05A396 396 0 0 1 342.95 488A396 396 0 0 1 396 290Z" />
        </svg>
        <span>sCloud</span>
      </Link>
      {project ? (
        <div className="scloud-topbar-project">
          <ProjectPicker projects={projects} project={project} onProjectChange={onProjectChange} />
        </div>
      ) : null}
      <div className="br-topbar-end">
        <div className="scloud-topbar-language"><LanguageSelect /></div>
        <UserMenu />
      </div>
    </div>
  );
}
