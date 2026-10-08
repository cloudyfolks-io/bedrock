import { useLingui } from "@lingui/react/macro";

export default function ConsoleTab() {
  const { t } = useLingui();
  return <p>{t({ id: "console.placeholder", message: "Console" })}</p>;
}
