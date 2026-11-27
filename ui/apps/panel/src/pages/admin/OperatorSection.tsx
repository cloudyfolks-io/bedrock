import { useId, type ReactNode } from "react";
import { Table } from "@bedrock/design";
import styles from "../operator.module.css";

export function OperatorSection({ title, summary, actions, children }: { title: string; summary?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  const heading = useId();
  return (
    <section className={styles.frame} aria-labelledby={heading}>
      <div className={styles.frameHeader}>
        <div className={styles.actions}>
          <h2 id={heading}>{title}</h2>
          {summary}
        </div>
        {actions}
      </div>
      <div className={styles.layer}>{children}</div>
    </section>
  );
}

export function OperatorTable({ title, children }: { title: string; children: ReactNode }) {
  return <OperatorSection title={title}><Table caption={title}>{children}</Table></OperatorSection>;
}
