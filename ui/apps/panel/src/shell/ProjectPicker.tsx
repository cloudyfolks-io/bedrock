import { useLingui } from "@lingui/react/macro";
import { Select } from "@base-ui/react/select";
import { projectOptions, type Project } from "../api/projects";
import styles from "./projectPicker.module.css";

function FolderIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M2 5a1.5 1.5 0 0 1 1.5-1.5h2.75L8 5.25h4.5A1.5 1.5 0 0 1 14 6.75v4.75a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 11.5Z" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
    </svg>
  );
}

export function ProjectPicker({ projects, project, onProjectChange }: { projects: readonly Project[]; project: string; onProjectChange: (name: string) => void }) {
  const { t } = useLingui();
  const label = t({ id: "shell.project.label", message: "Project" });
  const options = projectOptions(projects);

  return (
    <Select.Root items={options} value={project || null} onValueChange={(next) => onProjectChange(next ?? "")}>
      <Select.Trigger className={styles.trigger} aria-label={label}>
        <span className={styles.glyph}><FolderIcon /></span>
        <Select.Value className={styles.value} render={<bdi />} />
        <Select.Icon className={styles.chevrons} aria-hidden="true">
          <svg width="14" height="16" viewBox="0 0 14 16" fill="none">
            <path d="m4 6 3-3 3 3m-6 4 3 3 3-3" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Positioner className={styles.positioner} align="start" sideOffset={6} alignItemWithTrigger={false}>
          <Select.Popup className={styles.popup}>
            <Select.List className={styles.list} aria-label={label}>
              {options.map(({ value, label: name }) => (
                <Select.Item key={value} value={value} className={styles.option}>
                  <FolderIcon />
                  <Select.ItemText className={styles.optionName}><bdi>{name}</bdi></Select.ItemText>
                  <Select.ItemIndicator className={styles.check}>
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                      <path d="m3.5 8 3 3 6-6" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
                    </svg>
                  </Select.ItemIndicator>
                </Select.Item>
              ))}
            </Select.List>
          </Select.Popup>
        </Select.Positioner>
      </Select.Portal>
    </Select.Root>
  );
}
