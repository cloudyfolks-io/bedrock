import type { MessageDescriptor } from "@lingui/core";
import { useLingui } from "@lingui/react/macro";
import { PageHeader } from "../components/PageHeader";
import { useCurrentProject } from "../shell/context";

export function PlaceholderPage({ title }: { title: MessageDescriptor }) {
  const { i18n } = useLingui();
  const project = useCurrentProject();
  const label = i18n._(title);
  return <PageHeader title={label} breadcrumbs={project ? [{ label: project.displayName || project.name }, { label }] : [{ label }]} />;
}
