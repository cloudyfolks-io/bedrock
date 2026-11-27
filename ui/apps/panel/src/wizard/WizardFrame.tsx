import { useEffect, useRef, type ReactNode } from "react";
import { useBlocker, useNavigate, type Blocker } from "react-router";
import { useLingui } from "@lingui/react/macro";
import type { MessageDescriptor } from "@lingui/core";
import { Button } from "@bedrock/design";
import { ConfirmSimpleDialog } from "../components/ConfirmDialogs";
import { PageHeader } from "../components/PageHeader";
import styles from "./wizard.module.css";

export interface StepInfo {
  id: string;
  label: MessageDescriptor;
}

interface WizardFrameProps {
  title: string;
  steps: StepInfo[];
  index: number;
  blocker: Blocker;
  busy: boolean;
  nextDisabled?: boolean;
  errors: Record<string, string>;
  leaveTo: string;
  onBack: () => void;
  onNext: () => void;
  onSubmit: () => void;
  children: ReactNode;
}

export function useWizardNavigation(dirty: boolean) {
  const navigate = useNavigate();
  const accepted = useRef(false);
  const active = useRef(true);
  const blocker = useBlocker(({ currentLocation, nextLocation }) => dirty && !accepted.current && currentLocation.pathname !== nextLocation.pathname);

  useEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);

  useEffect(() => {
    if (!dirty) {
      return undefined;
    }
    const warn = (event: BeforeUnloadEvent) => {
      if (!accepted.current) {
        event.preventDefault();
        event.returnValue = true;
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  return {
    blocker,
    active,
    complete(to: string) {
      accepted.current = true;
      if (active.current) void navigate(to);
    },
  };
}

export function WizardFrame({ title, steps, index, blocker, busy, nextDisabled = false, errors, leaveTo, onBack, onNext, onSubmit, children }: WizardFrameProps) {
  const { t, i18n } = useLingui();
  const navigate = useNavigate();
  const heading = useRef<HTMLHeadingElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const confirmed = useRef<Blocker>(undefined);
  const last = index === steps.length - 1;
  const current = steps[index];

  useEffect(() => {
    heading.current?.focus();
  }, [index]);

  useEffect(() => {
    if (Object.keys(errors).length > 0) {
      form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    }
  }, [errors]);

  return (
    <>
      <PageHeader title={title} />
      <div className={styles.layout}>
        <ol className={styles.stepper} role="list" aria-label={t({ id: "wizard.steps", message: "Steps" })}>
          {steps.map((step, position) => (
            <li key={step.id} aria-current={position === index ? "step" : undefined} data-done={position < index ? "" : undefined}>
              {position + 1}. {i18n._(step.label)}
            </li>
          ))}
        </ol>
        <form
          ref={form}
          className={styles.form}
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            if (busy || nextDisabled) {
              return;
            }
            if (last) {
              onSubmit();
            } else {
              onNext();
            }
          }}
        >
          <div className={styles.fields}>
            <p className={styles.mobileProgress} aria-live="polite">{t({ id: "wizard.steps.progress", message: `Step ${index + 1} of ${steps.length}` })}</p>
            <h2 ref={heading} tabIndex={-1}>
              {current ? i18n._(current.label) : ""}
            </h2>
            {children}
          </div>
          <div className={styles.actions} aria-busy={busy}>
            <Button variant="secondary" onClick={() => void navigate(leaveTo)}>
              {t({ id: "common.action.cancel", message: "Cancel" })}
            </Button>
            <Button variant="secondary" disabled={index === 0 || busy} onClick={onBack}>
              <span aria-hidden="true" className="br-icon br-icon--directional">‹</span> {t({ id: "wizard.back", message: "Back" })}
            </Button>
            <Button variant="primary" type="submit" disabled={busy || nextDisabled}>
              {last ? t({ id: "wizard.create", message: "Create virtual machine" }) : t({ id: "wizard.next", message: "Next" })}{" "}
              {last ? null : <span aria-hidden="true" className="br-icon br-icon--directional">›</span>}
            </Button>
          </div>
        </form>
      </div>
      <ConfirmSimpleDialog
        open={blocker.state === "blocked"}
        title={t({ id: "wizard.leave.title", message: "Discard your changes?" })}
        description={busy
          ? t({ id: "wizard.leave.submitting", message: "Leaving does not cancel a request that was already sent." })
          : t({ id: "wizard.leave.description", message: "The virtual machine is not created yet." })}
        confirmLabel={t({ id: "wizard.leave.confirm", message: "Discard" })}
        onConfirm={() => {
          confirmed.current = blocker;
          blocker.proceed?.();
        }}
        onCancel={() => {
          // The dialog also closes after confirmation; don't reset a pending history transition.
          if (confirmed.current !== blocker) blocker.reset?.();
        }}
      />
    </>
  );
}
