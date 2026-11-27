import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../../api/types";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { listAt, textAt } from "../../resources/path";
import styles from "./project.module.css";

interface Attachment { subnetRef: string; securityGroups?: string[]; macAddress?: string }
interface Address { ip: string; mac?: string; subnet: string }

export function VmNetworksTab({ vm }: { vm: KubeObject }) {
  const { t } = useLingui();
  const addresses = listAt(vm, "status.addresses") as Address[];
  const attachments = listAt(vm, "spec.networks") as Attachment[];
  const elasticIp = textAt(vm, "status.elasticIP");
  return (
    <section className={styles.stack} aria-labelledby="vm-networks">
      <div className={styles.sectionHeading}>
        <h2 id="vm-networks">{t({ id: "vmDetail.networks", message: "Network attachments" })}</h2>
        {elasticIp ? <span>{t({ id: "col.elasticIp", message: "Elastic IP" })}: <Ltr>{elasticIp}</Ltr></span> : null}
      </div>
      {attachments.length ? (
        <ul className={styles.networks} role="list">
          {attachments.map((attachment) => {
            const address = addresses.find((entry) => entry.subnet === attachment.subnetRef);
            return (
              <li key={attachment.subnetRef}>
                <dl className={styles.facts}>
                  <div>
                    <dt>{t({ id: "col.subnet", message: "Subnet" })}</dt>
                    <dd><Ltr>{attachment.subnetRef}</Ltr></dd>
                  </div>
                  <div>
                    <dt>{t({ id: "col.address", message: "Address" })}</dt>
                    <dd className={styles.address}><Ltr>{address?.ip || "—"}</Ltr></dd>
                  </div>
                  <div>
                    <dt>MAC</dt>
                    <dd className={styles.address}><Ltr>{address?.mac || attachment.macAddress || "—"}</Ltr></dd>
                  </div>
                  <div>
                    <dt>{t({ id: "vmDetail.securityGroups", message: "Security groups" })}</dt>
                    <dd className={styles.values}>
                      {attachment.securityGroups?.length
                        ? attachment.securityGroups.map((group) => <Ltr key={group}>{group}</Ltr>)
                        : "—"}
                    </dd>
                  </div>
                </dl>
              </li>
            );
          })}
        </ul>
      ) : <EmptyState title={t({ id: "list.empty.title", message: "Nothing here yet" })} />}
    </section>
  );
}
