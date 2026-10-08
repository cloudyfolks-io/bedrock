import type { ReactNode } from "react";

interface EmptyStateProps {
  title: string;
  description?: string;
  action?: ReactNode;
}

export function EmptyState({ title, description, action }: EmptyStateProps) {
  return (
    <div className="br-empty">
      <h2 className="br-empty-title">{title}</h2>
      {description ? <p className="br-empty-description">{description}</p> : null}
      {action}
    </div>
  );
}
