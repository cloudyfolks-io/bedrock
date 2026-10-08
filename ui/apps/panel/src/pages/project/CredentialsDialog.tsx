import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useLingui } from "@lingui/react/macro";
import { Alert, Button, Dialog, useToast } from "@bedrock/design";
import { useHttp } from "../../api/HttpProvider";
import { kubeClient } from "../../api/kube";
import { Spinner } from "../../components/Spinner";
import { Ltr } from "../../components/Ltr";

const secrets = { group: "", version: "v1", plural: "secrets", namespaced: true } as const;
const secretKey = /secret|password|token/i;

function decode(value: string): string | undefined {
  try {
    const bytes = Uint8Array.from(atob(value), (character) => character.charCodeAt(0));
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return undefined;
  }
}

interface Props {
  namespace: string;
  secretName: string;
  open: boolean;
  onClose: () => void;
}

function Body({ namespace, secretName }: { namespace: string; secretName: string }) {
  const { t } = useLingui();
  const http = useHttp();
  const toast = useToast();
  const queryClient = useQueryClient();
  const key = ["credentials", namespace, secretName];
  const [revealed, setRevealed] = useState<ReadonlySet<string>>(new Set());
  const query = useQuery({ queryKey: key, gcTime: 0, staleTime: 0, retry: false, queryFn: () => kubeClient(http).get(secrets, namespace, secretName) });
  useEffect(() => () => queryClient.removeQueries({ queryKey: key }), []);

  if (query.isPending) {
    return <Spinner />;
  }
  if (query.isError) {
    return <Alert tone="error">{t({ id: "credentials.error", message: "The credentials did not load." })}</Alert>;
  }
  const data = (query.data as unknown as { data?: Record<string, string> }).data ?? {};
  const entries = Object.entries(data)
    .map(([name, encoded]) => [name, decode(encoded)] as const)
    .filter((entry): entry is readonly [string, string] => entry[1] !== undefined);

  return (
    <dl className="br-facts">
      {entries.map(([name, value]) => {
        const hidden = secretKey.test(name) && !revealed.has(name);
        return (
          <div key={name}>
            <dt>{name}</dt>
            <dd>
              <Ltr>{hidden ? "••••••••••••" : value}</Ltr>{" "}
              {secretKey.test(name) ? (
                <Button
                  variant="secondary"
                  onClick={() => setRevealed((current) => (current.has(name) ? new Set([...current].filter((entry) => entry !== name)) : new Set([...current, name])))}
                >
                  {revealed.has(name) ? t({ id: "credentials.hide", message: `Hide ${name}` }) : t({ id: "credentials.reveal", message: `Show ${name}` })}
                </Button>
              ) : null}
              <Button
                variant="secondary"
                onClick={() => {
                  void navigator.clipboard?.writeText(value);
                  toast.show({ tone: "info", title: t({ id: "credentials.copied", message: `${name} copied` }) });
                }}
              >
                {t({ id: "credentials.copy", message: `Copy ${name}` })}
              </Button>
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

export function CredentialsDialog({ namespace, secretName, open, onClose }: Props) {
  const { t } = useLingui();
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          onClose();
        }
      }}
      title={t({ id: "credentials.title", message: "Access credentials" })}
      description={t({ id: "credentials.description", message: "Keep these values private." })}
    >
      {open ? <Body namespace={namespace} secretName={secretName} /> : null}
    </Dialog>
  );
}
