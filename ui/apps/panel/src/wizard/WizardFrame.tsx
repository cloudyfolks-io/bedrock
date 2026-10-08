import { useEffect, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { useLingui } from "@lingui/react/macro";
import type { MessageDescriptor } from "@lingui/core";
import { Button } from "@bedrock/design";
import { ConfirmSimpleDialog } from "../components/ConfirmDialogs";
import { PageHeader } from "../components/PageHeader";

export interface StepInfo {
  id: string;
  label: MessageDescriptor;
}

interface WizardFrameProps {
  title: string;
  steps: StepInfo[];
  index: number;
  dirty: boolean;
  busy: boolean;
  leaveTo: string;
  onBack: () => void;
  onNext: () => void;
  onSubmit: () => void;
  children: ReactNode;
}

export function WizardFrame({ title, steps, index, dirty, busy, leaveTo, onBack, onNext, onSubmit, children }: WizardFrameProps) {
  const { t, i18n } = useLingui();
  const navigate = useNavigate();
  const heading = useRef<HTMLHeadingElement>(null);
  const [leaving, setLeaving] = useState(false);
  const last = index === steps.length - 1;
  const current = steps[index];

  useEffect(() => {
    heading.current?.focus();
  }, [index]);

  useEffect(() => {
    if (!dirty) {
      return undefined;
    }
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  return (
    <>
      <PageHeader title={title} />
      <ol className="br-stepper" aria-label={t({ id: "wizard.steps", message: "Steps" })}>
        {steps.map((step, position) => (
          <li key={step.id} className="br-stepper-item" aria-current={position === index ? "step" : undefined} data-done={position < index ? "" : undefined}>
            {position + 1}. {i18n._(step.label)}
          </li>
        ))}
      </ol>
      <form
        className="br-wizard"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          if (last) {
            onSubmit();
          } else {
            onNext();
          }
        }}
      >
        <h2 ref={heading} tabIndex={-1}>
          {current ? i18n._(current.label) : ""}
        </h2>
        {children}
        <div className="br-dialog-actions">
          <Button variant="secondary" onClick={() => (dirty ? setLeaving(true) : void navigate(leaveTo))}>
            {t({ id: "common.action.cancel", message: "Cancel" })}
          </Button>
          <Button variant="secondary" disabled={index === 0} onClick={onBack}>
            <span aria-hidden="true" className="br-icon br-icon--directional">‹</span> {t({ id: "wizard.back", message: "Back" })}
          </Button>
          <Button variant="primary" type="submit" disabled={busy}>
            {last ? t({ id: "wizard.create", message: "Create virtual machine" }) : t({ id: "wizard.next", message: "Next" })}{" "}
            {last ? null : <span aria-hidden="true" className="br-icon br-icon--directional">›</span>}
          </Button>
        </div>
      </form>
      <ConfirmSimpleDialog
        open={leaving}
        title={t({ id: "wizard.leave.title", message: "Discard your changes?" })}
        description={t({ id: "wizard.leave.description", message: "The virtual machine is not created yet." })}
        confirmLabel={t({ id: "wizard.leave.confirm", message: "Discard" })}
        onConfirm={() => void navigate(leaveTo)}
        onCancel={() => setLeaving(false)}
      />
    </>
  );
}
