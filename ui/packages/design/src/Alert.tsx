import type { ReactNode } from "react";

interface AlertProps {
  tone: "error" | "info";
  children: ReactNode;
}

export function Alert({ tone, children }: AlertProps) {
  return (
    <div className={`br-alert br-alert--${tone}`} role={tone === "error" ? "alert" : "status"}>
      {children}
    </div>
  );
}
