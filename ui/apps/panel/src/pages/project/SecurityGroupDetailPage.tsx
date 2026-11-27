import { useState } from "react";
import { useParams } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Badge, Button, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { EmptyState } from "../../components/EmptyState";
import { Ltr } from "../../components/Ltr";
import { PageHeader } from "../../components/PageHeader";
import { QueryFailure } from "../../components/QueryFailure";
import { Spinner } from "../../components/Spinner";
import { LazyEditDialog } from "../../forms/LazyDialogs";
import { listAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { exposesSsh, rulePorts, type SecurityGroupRule } from "../../resources/summaries";
import { useCurrentProject } from "../../shell/context";
import { useLiveObject } from "../../stream/live";
import styles from "./project.module.css";
import rulesStyles from "./securityGroup.module.css";

function RulesTable({ caption, rules, direction }: { caption: string; rules: SecurityGroupRule[]; direction: "ingress" | "egress" }) {
  const { t } = useLingui();
  return (
    <Table caption={caption}>
      <TableHead>
        <TableRow>
          <TableHeaderCell>{t({ id: "sg.protocol", message: "Protocol" })}</TableHeaderCell>
          <TableHeaderCell>{t({ id: "sg.ports", message: "Ports" })}</TableHeaderCell>
          <TableHeaderCell>{direction === "ingress" ? t({ id: "col.source", message: "Source" }) : t({ id: "col.destination", message: "Destination" })}</TableHeaderCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {rules.length === 0 ? (
          <TableRow><td className={`br-td ${styles.emptyRow}`} colSpan={3}>{t({ id: "list.empty.title", message: "Nothing here yet" })}</td></TableRow>
        ) : rules.map((rule, index) => (
          <TableRow key={index}>
            <TableCell>{rule.protocol ?? "all"}</TableCell>
            <TableCell><Ltr>{rulePorts(rule)}</Ltr></TableCell>
            <TableCell>
              <div className={rulesStyles.peer}>
                <Ltr>{rule.cidr ?? rule.securityGroupRef ?? "—"}</Ltr>
                {direction === "ingress" && exposesSsh(rule) ? <Badge tone="warning">{t({ id: "sg.openSsh", message: "Open to the internet" })}</Badge> : null}
              </div>
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
  const breadcrumbs = [{ label: i18n._(def.title), to: `/p/${project.name}/security-groups` }, { label: name }];
  if (query.isError) {
    return (
      <>
        <PageHeader title={name} breadcrumbs={breadcrumbs} />
        <QueryFailure error={query.error} retry={() => void query.refetch()} />
      </>
    );
  }
  const group: KubeObject | null | undefined = query.data;
  if (!group) {
    return (
      <>
        <PageHeader title={name} breadcrumbs={breadcrumbs} />
        <EmptyState title={t({ id: "detail.deleted.title", message: "This object was deleted" })} />
      </>
    );
  }
  return (
    <div className={`${styles.stack} ${styles.tables} ${rulesStyles.tables}`}>
      <PageHeader
        title={name}
        breadcrumbs={breadcrumbs}
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
        direction="ingress"
        rules={listAt(group, "spec.ingress") as SecurityGroupRule[]}
      />
      <RulesTable caption={t({ id: "col.egress", message: "Egress rules" })} direction="egress" rules={listAt(group, "spec.egress") as SecurityGroupRule[]} />
      {editing ? <LazyEditDialog def={def} object={group} onClose={() => setEditing(false)} /> : null}
    </div>
  );
}
