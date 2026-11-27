import { useSyncExternalStore, type ReactNode } from "react";
import styles from "./resources.module.css";

const breakpoint = "(max-width: 42rem)";
const wideFields = new Set(["addresses", "address", "cidr", "digest", "endpoint", "host", "image", "records"]);
const media = () => typeof window === "undefined" ? undefined : window.matchMedia?.(breakpoint);

function subscribe(update: () => void) {
  const query = media();
  query?.addEventListener("change", update);
  return () => query?.removeEventListener("change", update);
}

export function useCompactResourceRows() {
  return useSyncExternalStore(subscribe, () => media()?.matches ?? false, () => false);
}

export function CompactResourceRows({ label, children }: { label: string; children: ReactNode }) {
  return <ul className={styles.compactRows} role="list" aria-label={label}>{children}</ul>;
}

export function CompactResourceRow({ name, status, actions, fields }: {
  name: ReactNode;
  status: ReactNode;
  actions?: ReactNode;
  fields: { id: string; label: string; value: ReactNode }[];
}) {
  const available = fields.filter(({ value }) => value != null && value !== "" && value !== "—");
  return (
    <li className={styles.compactRow}>
      <div className={styles.compactRowHead}>
        <strong>{name}</strong>
        <div>{status}{actions}</div>
      </div>
      {available.length > 0 ? (
        <dl className={styles.compactFields}>
          {available.map(({ id, label, value }) => <div key={id} data-wide={wideFields.has(id) || undefined}><dt>{label}</dt><dd>{value}</dd></div>)}
        </dl>
      ) : null}
    </li>
  );
}
