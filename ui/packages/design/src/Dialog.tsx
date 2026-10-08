import type { ReactNode } from "react";
import { Dialog as BaseDialog } from "@base-ui/react/dialog";

interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  actions?: ReactNode;
}

export function Dialog({ open, onOpenChange, title, description, children, actions }: DialogProps) {
  return (
    <BaseDialog.Root open={open} onOpenChange={onOpenChange}>
      <BaseDialog.Portal>
        <BaseDialog.Backdrop className="br-backdrop" />
        <BaseDialog.Popup className="br-dialog">
          <BaseDialog.Title className="br-dialog-title">{title}</BaseDialog.Title>
          {description ? <BaseDialog.Description className="br-dialog-description">{description}</BaseDialog.Description> : null}
          <div className="br-dialog-body">{children}</div>
          <div className="br-dialog-actions">
            {actions}
            <BaseDialog.Close className="br-button br-button--secondary">×</BaseDialog.Close>
          </div>
        </BaseDialog.Popup>
      </BaseDialog.Portal>
    </BaseDialog.Root>
  );
}
