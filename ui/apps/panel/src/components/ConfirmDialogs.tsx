import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { AlertDialog, Button, Dialog, TextField } from "@bedrock/design";

interface ConfirmDeleteProps {
  name: string;
  kind: string;
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

function DeleteBody({ name, kind, onConfirm, onCancel }: Omit<ConfirmDeleteProps, "open">) {
  const { t } = useLingui();
  const [typed, setTyped] = useState("");
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onCancel();
        }
      }}
      title={t({ id: "confirm.delete.title", message: `Delete ${kind}` })}
      description={t({ id: "confirm.delete.description", message: `This cannot be undone. Type the name ${name} to confirm.` })}
      actions={
        <Button variant="danger" disabled={typed !== name} onClick={onConfirm}>
          {t({ id: "common.action.delete", message: "Delete" })}
        </Button>
      }
    >
      <TextField label={t({ id: "confirm.delete.input", message: "Name" })} name="confirm-name" autoComplete="off" value={typed} onChange={setTyped} />
    </Dialog>
  );
}

export function ConfirmDeleteDialog(props: ConfirmDeleteProps) {
  return props.open ? <DeleteBody name={props.name} kind={props.kind} onConfirm={props.onConfirm} onCancel={props.onCancel} /> : null;
}

interface ConfirmSimpleProps {
  title: string;
  description: string;
  confirmLabel: string;
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmSimpleDialog({ title, description, confirmLabel, open, onConfirm, onCancel }: ConfirmSimpleProps) {
  const { t } = useLingui();
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          onCancel();
        }
      }}
      title={title}
      description={description}
      confirmLabel={confirmLabel}
      cancelLabel={t({ id: "common.action.cancel", message: "Cancel" })}
      onConfirm={onConfirm}
      tone="neutral"
    />
  );
}
