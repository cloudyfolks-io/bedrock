import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { Ltr } from "../../components/Ltr";
import { listAt, textAt } from "../../resources/path";

interface Disk { volumeRef: string; bus?: string }

export function VmDisksTab({ vm, project }: { vm: KubeObject; project: string }) {
  const { t } = useLingui();
  return (
    <>
      <dl className="br-facts">
        <dt>{t({ id: "vmDetail.rootDisk", message: "Root disk" })}</dt>
        <dd>{textAt(vm, "spec.rootDisk.sizeGi")} GiB</dd>
        <dt>{t({ id: "col.storageClass", message: "Storage class" })}</dt>
        <dd><Ltr>{textAt(vm, "spec.rootDisk.storageClass") || "—"}</Ltr></dd>
      </dl>
      <Table caption={t({ id: "vmDetail.volumes", message: "Attached volumes" })}>
        <TableHead>
          <TableRow>
            <TableHeaderCell>{t({ id: "col.volume", message: "Volume" })}</TableHeaderCell>
            <TableHeaderCell>{t({ id: "vmDetail.bus", message: "Bus" })}</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {(listAt(vm, "spec.disks") as Disk[]).map((disk) => (
            <TableRow key={disk.volumeRef}>
              <TableCell><Link to={`/p/${project}/volumes/${disk.volumeRef}`}>{disk.volumeRef}</Link></TableCell>
              <TableCell>{disk.bus ?? "virtio"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </>
  );
}
