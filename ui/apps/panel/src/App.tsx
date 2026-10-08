import { useLingui } from "@lingui/react/macro";
import { LocaleProvider } from "./LocaleProvider";

function Title() {
  const { t } = useLingui();
  return <h1>{t({ id: "app.title", message: "Bedrock" })}</h1>;
}

export function App() {
  return (
    <LocaleProvider>
      <Title />
    </LocaleProvider>
  );
}
