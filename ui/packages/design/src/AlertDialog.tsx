import { useRef } from "react";
import { AlertDialog as BaseAlertDialog } from "@base-ui/react/alert-dialog";

interface AlertDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  cancelLabel: string;
  onConfirm: () => void;
  tone?: "danger" | "neutral";
}

export function AlertDialog({ open, onOpenChange, title, description, confirmLabel, cancelLabel, onConfirm, tone }: AlertDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const effectiveTone = tone ?? "neutral";
  return (
    <BaseAlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <BaseAlertDialog.Portal>
        <BaseAlertDialog.Backdrop className="br-backdrop" />
        <BaseAlertDialog.Popup className="br-dialog" initialFocus={effectiveTone === "danger" ? cancelRef : true}>
          <BaseAlertDialog.Title className="br-dialog-title">{title}</BaseAlertDialog.Title>
          <BaseAlertDialog.Description className="br-dialog-description">{description}</BaseAlertDialog.Description>
          <div className="br-dialog-actions">
            <BaseAlertDialog.Close ref={cancelRef} className="br-button br-button--secondary">
              {cancelLabel}
            </BaseAlertDialog.Close>
            <button
              type="button"
              className={`br-button br-button--${effectiveTone === "danger" ? "danger" : "primary"}`}
              onClick={() => {
                onConfirm();
                onOpenChange(false);
              }}
            >
              {confirmLabel}
            </button>
          </div>
        </BaseAlertDialog.Popup>
      </BaseAlertDialog.Portal>
    </BaseAlertDialog.Root>
  );
}
