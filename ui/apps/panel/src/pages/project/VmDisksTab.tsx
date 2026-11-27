import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { Ltr } from "../../components/Ltr";
import { listAt, textAt } from "../../resources/path";
import styles from "./project.module.css";

interface Disk { volumeRef: string; bus?: string }

export function VmDisksTab({ vm, project }: { vm: KubeObject; project: string }) {
  const { t } = useLingui();
  const disks = listAt(vm, "spec.disks") as Disk[];
  const rootVolume = textAt(vm, "spec.rootDisk.volumeRef");
  const size = textAt(vm, "spec.rootDisk.sizeGi");
  return (
    <div className={`${styles.stack} ${styles.tables}`}>
      <dl className={styles.facts}>
        <div>
          <dt>{t({ id: "vmDetail.rootDisk", message: "Root disk" })}</dt>
          <dd>
            {rootVolume
              ? <Link to={`/p/${project}/volumes/${rootVolume}`}><Ltr>{rootVolume}</Ltr></Link>
              : <Ltr>{size ? `${size} GiB` : "—"}</Ltr>}
          </dd>
        </div>
        {rootVolume ? null : (
          <div>
            <dt>{t({ id: "col.storageClass", message: "Storage class" })}</dt>
            <dd><Ltr>{textAt(vm, "spec.rootDisk.storageClass") || "—"}</Ltr></dd>
          </div>
        )}
      </dl>
      <Table caption={t({ id: "vmDetail.volumes", message: "Attached volumes" })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "col.volume", message: "Volume" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "vmDetail.bus", message: "Bus" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {disks.length === 0 ? (
            <TableRow><td className={`br-td ${styles.emptyRow}`} colSpan={2}>{t({ id: "list.empty.title", message: "Nothing here yet" })}</td></TableRow>
          ) : disks.map((disk) => (
            <TableRow key={disk.volumeRef}>
              <TableCell><Link to={`/p/${project}/volumes/${disk.volumeRef}`}><Ltr>{disk.volumeRef}</Ltr></Link></TableCell>
              <TableCell>{disk.bus ?? "virtio"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
