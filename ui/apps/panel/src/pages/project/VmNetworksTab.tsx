import { useLingui } from "@lingui/react/macro";
import { Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { Ltr } from "../../components/Ltr";
import { listAt, textAt } from "../../resources/path";

interface Attachment { subnetRef: string; securityGroups?: string[]; macAddress?: string }
interface Address { ip: string; mac?: string; subnet: string }

export function VmNetworksTab({ vm }: { vm: KubeObject }) {
  const { t } = useLingui();
  const addresses = listAt(vm, "status.addresses") as Address[];
  return (
    <Table caption={t({ id: "vmDetail.networks", message: "Network attachments" })}>
      <TableHead>
        <TableRow>
          <TableHeaderCell>{t({ id: "col.subnet", message: "Subnet" })}</TableHeaderCell>
          <TableHeaderCell>{t({ id: "vmDetail.securityGroups", message: "Security groups" })}</TableHeaderCell>
          <TableHeaderCell>{t({ id: "col.address", message: "Address" })}</TableHeaderCell>
          <TableHeaderCell>MAC</TableHeaderCell>
          <TableHeaderCell>{t({ id: "col.elasticIp", message: "Elastic IP" })}</TableHeaderCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {(listAt(vm, "spec.networks") as Attachment[]).map((attachment) => {
          const address = addresses.find((entry) => entry.subnet === attachment.subnetRef);
          return (
            <TableRow key={attachment.subnetRef}>
              <TableCell>{attachment.subnetRef}</TableCell>
              <TableCell>{(attachment.securityGroups ?? []).join(", ") || "—"}</TableCell>
              <TableCell>{address ? <Ltr>{address.ip}</Ltr> : "—"}</TableCell>
              <TableCell>{address?.mac || attachment.macAddress ? <Ltr>{address?.mac ?? attachment.macAddress ?? ""}</Ltr> : "—"}</TableCell>
              <TableCell>{textAt(vm, "status.elasticIP") || "—"}</TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
