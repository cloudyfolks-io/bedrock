import type { ReactNode } from "react";

export function Ltr({ children }: { children: ReactNode }) {
  return (
    <bdi dir="ltr" className="br-ltr">
      {children}
    </bdi>
  );
}
