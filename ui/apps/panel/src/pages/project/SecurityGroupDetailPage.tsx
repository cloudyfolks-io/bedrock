import { useState, type ReactNode } from "react";
import { useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { Spinner } from "../../components/Spinner";
import { LazyEditDialog } from "../../forms/LazyDialogs";
import { listAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { exposesSsh, ruleSummary } from "../../resources/summaries";
import { useCurrentProject } from "../../shell/context";
import { useLiveObject } from "../../stream/live";

interface Rule { protocol?: string; port?: number; endPort?: number; cidr?: string; securityGroupRef?: string }

function RulesTable({ caption, rules, annotate }: { caption: string; rules: Rule[]; annotate?: (rule: Rule) => ReactNode }) {
  const { t } = useLingui();
  return (
    <Table caption={caption}>
      <TableHead>
        <TableRow>
          <TableHeaderCell>{t({ id: "sg.protocol", message: "Protocol" })}</TableHeaderCell>
          <TableHeaderCell>{t({ id: "sg.ports", message: "Ports" })}</TableHeaderCell>
          <TableHeaderCell>{t({ id: "sg.source", message: "CIDR or security group" })}</TableHeaderCell>
          <TableHeaderCell>{t({ id: "sg.summary", message: "Summary" })}</TableHeaderCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {rules.map((rule, index) => (
          <TableRow key={`${ruleSummary(rule)}-${index}`}>
            <TableCell>{rule.protocol ?? "all"}</TableCell>
            <TableCell><Ltr>{rule.port === undefined ? "—" : rule.endPort === undefined ? String(rule.port) : `${rule.port}-${rule.endPort}`}</Ltr></TableCell>
            <TableCell><Ltr>{rule.cidr ?? rule.securityGroupRef ?? "—"}</Ltr></TableCell>
            <TableCell>
              <Ltr>{ruleSummary(rule)}</Ltr>{" "}
              {annotate?.(rule)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export default function SecurityGroupDetailPage() {
  const { t, i18n } = useLingui();
  const { name = "" } = useParams();
  const def = definitionFor("securitygroups");
  const project = useCurrentProject();
  const ref = def ? refOf(def) : { group: "", version: "", plural: "", namespaced: true };
  const query = useLiveObject(ref, project?.namespace, name);
  const [editing, setEditing] = useState(false);
  const [canUpdate] = useCans(project ? [{ verb: "update", group: ref.group, resource: ref.plural, namespace: project.namespace, name }] : []);
  if (!def || !project || query.isPending) {
    return <Spinner />;
  }
  const group: KubeObject | null | undefined = query.data;
  if (!group) {
    return <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />;
  }
  return (
    <>
      <PageHeader
        title={name}
        breadcrumbs={[{ label: i18n._(def.title), to: `/p/${project.name}/security-groups` }, { label: name }]}
        actions={
          canUpdate === "allowed" ? (
            <Button variant="secondary" onClick={() => setEditing(true)}>
              {t({ id: "sg.editRules", message: "Edit rules" })}
            </Button>
          ) : null
        }
      />
      <RulesTable
        caption={t({ id: "col.ingress", message: "Ingress rules" })}
        rules={listAt(group, "spec.ingress") as Rule[]}
        annotate={(rule) => (exposesSsh(rule) ? <Badge tone="warning">{t({ id: "sg.openSsh", message: "Open to the internet" })}</Badge> : null)}
      />
      <RulesTable caption={t({ id: "col.egress", message: "Egress rules" })} rules={listAt(group, "spec.egress") as Rule[]} />
      {editing ? <LazyEditDialog def={def} object={group} onClose={() => setEditing(false)} /> : null}
    </>
  );
}
