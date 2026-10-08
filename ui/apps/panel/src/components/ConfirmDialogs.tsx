import { useState } from "react";
import { useLingui } from "@lingui/react/macro";
import { AlertDialog, Button, Dialog, TextField } from "@bedrock/design";

interface ConfirmTypedProps {
  word: string;
  title: string;
  description: string;
  confirmLabel: string;
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

function TypedBody({ word, title, description, confirmLabel, onConfirm, onCancel }: Omit<ConfirmTypedProps, "open">) {
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
      title={title}
      description={description}
      actions={
        <Button variant="danger" disabled={typed !== word} onClick={onConfirm}>
          {confirmLabel}
        </Button>
      }
    >
      <TextField label={t({ id: "confirm.delete.input", message: "Name" })} name="confirm-name" autoComplete="off" value={typed} onChange={setTyped} />
    </Dialog>
  );
}

export function ConfirmTypedDialog(props: ConfirmTypedProps) {
  return props.open ? <TypedBody {...props} /> : null;
}

interface ConfirmDeleteProps {
  name: string;
  kind: string;
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDeleteDialog({ name, kind, open, onConfirm, onCancel }: ConfirmDeleteProps) {
  const { t } = useLingui();
  return (
    <ConfirmTypedDialog
      word={name}
      title={t({ id: "confirm.delete.title", message: `Delete ${kind}` })}
      description={t({ id: "confirm.delete.description", message: `This cannot be undone. Type the name ${name} to confirm.` })}
      confirmLabel={t({ id: "common.action.delete", message: "Delete" })}
      open={open}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
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
