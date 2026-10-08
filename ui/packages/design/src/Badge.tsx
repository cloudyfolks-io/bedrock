import type { ReactNode } from "react";

interface BadgeProps {
  tone: "neutral" | "success" | "warning" | "danger" | "info";
  children: ReactNode;
}

export function Badge({ tone, children }: BadgeProps) {
  return (
    <span className="br-badge" data-tone={tone}>
      {children}
    </span>
  );
}
