import { useLingui } from "@lingui/react/macro";
import { Badge } from "@bedrock/design";
import { useHttp } from "../api/HttpProvider";
import { usePendingWrites, type PendingWrite } from "../api/pendingWrites";
import styles from "../resources/resources.module.css";

export function useCurrentPendingWrites() {
  return usePendingWrites(useHttp());
}

export function PendingWriteTag({ kind }: { kind: PendingWrite["kind"] }) {
  const { t } = useLingui();
  const labels = {
    create: t({ id: "pendingWrites.create", message: "Creating" }),
    update: t({ id: "pendingWrites.update", message: "Saving" }),
    delete: t({ id: "pendingWrites.delete", message: "Deleting" }),
    action: t({ id: "pendingWrites.action", message: "Requesting action" }),
  };
  return (
    <span className={styles.pendingTag}>
      <Badge tone="info">{labels[kind]}</Badge>
    </span>
  );
}

export function PendingWrites() {
  const { t } = useLingui();
  const writes = useCurrentPendingWrites();
  return (
    <div className={styles.pendingIndicator} role="status">
      {writes.length > 0 ? <Badge tone="info">{t({ id: "pendingWrites.global", message: "Changes in progress" })}</Badge> : null}
    </div>
  );
}
