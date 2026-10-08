import { useLingui } from "@lingui/react/macro";

export function Spinner() {
  const { t } = useLingui();
  return (
    <div role="status" className="br-spinner">
      <span className="br-spinner-dot" aria-hidden="true" />
      <span className="br-visually-hidden">{t({ id: "common.state.loading", message: "Loading" })}</span>
    </div>
  );
}
