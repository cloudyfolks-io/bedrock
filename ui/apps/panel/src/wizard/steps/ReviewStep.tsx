import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../../api/types";
import { vmSize } from "../../resources/vmSize";
import type { DiskValues, IsoValues } from "../types";
import styles from "../wizard.module.css";

export function ReviewStep({ values, object }: { values: DiskValues | IsoValues; object: KubeObject }) {
  const { t } = useLingui();
  const isIso = "isoName" in values;
  const none = t({ id: "wizard.review.none", message: "None selected" });
  const defaults = t({ id: "col.default", message: "Default" });
  const showNames = (names: string[]) => names.length ? <bdi dir="ltr">{names.join(", ")}</bdi> : none;
  return (
    <>
      <div className={styles.reviewGrid}>
        <section className={styles.reviewSection}>
          <h3>{t({ id: "wizard.step.basics", message: "Basics" })}</h3>
          <dl className={styles.facts}>
            <dt>{t({ id: "forms.name.label", message: "Name" })}</dt>
            <dd><bdi dir="ltr">{values.name}</bdi></dd>
            <dt>{values.customSize ? t({ id: "wizard.customSize", message: "Custom size" }) : t({ id: "col.instanceType", message: "Instance type" })}</dt>
            <dd><bdi dir="ltr">{vmSize(object)}</bdi></dd>
            <dt>{t({ id: "wizard.startAfter", message: "Start after creation" })}</dt>
            <dd>{values.running ? t({ id: "common.yes", message: "Yes" }) : t({ id: "common.no", message: "No" })}</dd>
          </dl>
        </section>
        <section className={styles.reviewSection}>
          <h3>{t({ id: "wizard.review.boot", message: "Boot" })}</h3>
          <dl className={styles.facts}>
            <dt>{isIso ? t({ id: "wizard.iso", message: "Installation ISO" }) : t({ id: "wizard.step.image", message: "Image" })}</dt>
            <dd>
              {isIso ? <bdi dir="ltr">{values.isoName}</bdi> : (
                <>
                  <bdi dir="ltr">{values.imageName}</bdi>{" · "}
                  {values.imageScope === "cluster" ? t({ id: "images.tab.catalog", message: "Catalog" }) : t({ id: "images.tab.project", message: "Project images" })}
                </>
              )}
            </dd>
            <dt>{t({ id: "wizard.review.bootOrder", message: "Boot order" })}</dt>
            <dd>{isIso
              ? t({ id: "wizard.review.bootOrderIso", message: "Root disk, then CD-ROMs" })
              : t({ id: "vmDetail.rootDisk", message: "Root disk" })}</dd>
            {isIso ? (
              <>
                <dt>{t({ id: "wizard.step.firmware", message: "Firmware" })}</dt>
                <dd>{values.firmware.uefi ? "UEFI" : "BIOS"}</dd>
              </>
            ) : null}
          </dl>
        </section>
        <section className={styles.reviewSection}>
          <h3>{t({ id: "wizard.step.disks", message: "Disks" })}</h3>
          <dl className={styles.facts}>
            <dt>{t({ id: "vmDetail.rootDisk", message: "Root disk" })}</dt>
            <dd><bdi dir="ltr">{values.rootDiskGi} GiB</bdi></dd>
            <dt>{t({ id: "col.storageClass", message: "Storage class" })}</dt>
            <dd>{values.storageClass ? <bdi dir="ltr">{values.storageClass}</bdi> : defaults}</dd>
            <dt>{isIso ? t({ id: "wizard.review.extraCdroms", message: "Additional CD-ROMs" }) : t({ id: "wizard.review.extraVolumes", message: "Existing volumes" })}</dt>
            <dd>{showNames(isIso ? values.extraCdroms : values.extraVolumes)}</dd>
          </dl>
        </section>
        <section className={styles.reviewSection}>
          <h3>{t({ id: "wizard.step.network", message: "Network" })}</h3>
          <dl className={styles.facts}>
            <dt>{t({ id: "col.subnet", message: "Subnet" })}</dt>
            <dd><bdi dir="ltr">{values.subnetRef}</bdi></dd>
            <dt>{t({ id: "vmDetail.securityGroups", message: "Security groups" })}</dt>
            <dd>{showNames(values.securityGroups)}</dd>
          </dl>
        </section>
      </div>
      <details className={styles.advanced}>
        <summary>{t({ id: "wizard.review.advanced", message: "Advanced specification (JSON)" })}</summary>
        <pre dir="ltr" className={`br-code-block ${styles.review}`} tabIndex={0}>
          {JSON.stringify(object.spec, null, 2)}
        </pre>
      </details>
    </>
  );
}
