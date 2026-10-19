import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../../api/types";
import { vmSize } from "../../resources/vmSize";

export function ReviewStep({ object }: { object: KubeObject }) {
  const { t } = useLingui();
  return (
    <>
      <dl className="br-facts">
        <dt>{t({ id: "forms.name.label", message: "Name" })}</dt>
        <dd dir="ltr">{object.metadata.name}</dd>
        <dt>{t({ id: "col.instanceType", message: "Instance type" })}</dt>
        <dd dir="ltr">{vmSize(object)}</dd>
      </dl>
      <pre dir="ltr" className="br-code-block">
        {JSON.stringify(object.spec, null, 2)}
      </pre>
    </>
  );
}
