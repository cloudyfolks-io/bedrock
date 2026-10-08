import { useLingui } from "@lingui/react/macro";
import type { KubeObject } from "../../api/types";

export function ReviewStep({ object }: { object: KubeObject }) {
  const { t } = useLingui();
  const spec = (object.spec ?? {}) as Record<string, unknown>;
  return (
    <>
      <dl className="br-facts">
        <dt>{t({ id: "forms.name.label", message: "Name" })}</dt>
        <dd dir="ltr">{object.metadata.name}</dd>
        <dt>{t({ id: "col.instanceType", message: "Instance type" })}</dt>
        <dd dir="ltr">{String(spec.instanceType ?? "")}</dd>
      </dl>
      <pre dir="ltr" className="br-code-block">
        {JSON.stringify(object.spec, null, 2)}
      </pre>
    </>
  );
}
