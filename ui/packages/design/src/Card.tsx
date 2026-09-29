import type { ReactNode } from "react";

interface CardProps {
  title: string;
  children: ReactNode;
}

export function Card({ title, children }: CardProps) {
  return (
    <section className="br-card">
      <h2 className="br-card-title">{title}</h2>
      {children}
    </section>
  );
}
