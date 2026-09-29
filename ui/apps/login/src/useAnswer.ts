import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { ApiError } from "./api";
import type { Challenge } from "./types";

export interface UseAnswer {
  error: string | null;
  submit: (promise: Promise<Challenge>) => void;
  fail: (reason?: unknown) => void;
}

export function codeFromRejection(reason: unknown): string {
  return reason instanceof ApiError ? reason.code : "unknown";
}

export function useAnswer(onChallenge: (next: Challenge) => void): UseAnswer {
  const [error, setError] = useState<string | null>(null);

  const fail = (reason?: unknown) => setError(codeFromRejection(reason));

  const submit = (promise: Promise<Challenge>) => {
    promise
      .then((next) => {
        if (next.type === "error") {
          setError(next.error?.code ?? "unknown");
          return;
        }
        onChallenge(next);
      })
      .catch(fail);
  };

  return { error, submit, fail };
}

export function useFocusFirstField(): RefObject<HTMLDivElement | null> {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    ref.current?.querySelector<HTMLElement>("input, textarea")?.focus();
  }, []);

  return ref;
}

export function useFocusOnError(active: boolean): RefObject<HTMLDivElement | null> {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!active) {
      return;
    }
    const alert = ref.current?.querySelector<HTMLElement>('[role="alert"]');
    if (alert) {
      alert.tabIndex = -1;
      alert.focus();
    }
  }, [active]);

  return ref;
}
