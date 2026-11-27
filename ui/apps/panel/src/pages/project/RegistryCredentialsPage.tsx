import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { Button } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { textAt } from "../../resources/path";
import { definitionFor } from "../../resources/registry";
import { ResourceDetailPage } from "../../resources/ResourceDetailPage";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { CredentialsDialog } from "./CredentialsDialog";

const hiddenKeys = [".dockerconfigjson"];

function useRegistryLogin() {
  const { t } = useLingui();
  const project = useCurrentProject();
  const namespace = project?.namespace;
  const [showing, setShowing] = useState<{ namespace: string; name: string; secretName: string }>();
  const [canRead] = useCans(namespace ? [{ verb: "get", group: "", resource: "secrets", namespace }] : []);
  return {
    label: t({ id: "registry.showLogin", message: "Show login" }),
    available: (object: KubeObject) => canRead === "allowed" && textAt(object, "status.secretName") !== "",
    show: (object: KubeObject) => {
      const secretName = textAt(object, "status.secretName");
      if (namespace && canRead === "allowed" && secretName) setShowing({ namespace, name: object.metadata.name, secretName });
    },
    dialog: (object?: KubeObject) => showing && canRead === "allowed" && showing.namespace === namespace &&
      (!object || (showing.name === object.metadata.name && showing.secretName === textAt(object, "status.secretName")))
      ? <CredentialsDialog namespace={showing.namespace} secretName={showing.secretName} hidden={hiddenKeys} open onClose={() => setShowing(undefined)} />
      : null,
  };
}

export default function RegistryCredentialsPage() {
  const def = definitionFor("registrycredentials");
  const login = useRegistryLogin();
  if (!def) {
    return null;
  }
  return (
    <ResourceListPage
      def={def}
      rowItems={(object) =>
        login.available(object)
          ? [{ id: "login", label: login.label, onSelect: () => login.show(object) }]
          : []
      }
    >
      {login.dialog()}
    </ResourceListPage>
  );
}

function RegistryLoginAction({ object }: { object: KubeObject }) {
  const login = useRegistryLogin();
  if (!login.available(object)) return null;
  return (
    <>
      <Button variant="secondary" onClick={() => login.show(object)}>{login.label}</Button>
      {login.dialog(object)}
    </>
  );
}

export function RegistryCredentialDetailPage() {
  const def = definitionFor("registrycredentials");
  return def ? (
    <ResourceDetailPage def={def} actions={(object) => (
      <RegistryLoginAction key={`${object.metadata.namespace}/${object.metadata.uid ?? object.metadata.name}/${textAt(object, "status.secretName")}`} object={object} />
    )} />
  ) : null;
}
