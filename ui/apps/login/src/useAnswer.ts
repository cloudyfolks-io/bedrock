import { useState } from "react";
import type { Challenge } from "./types";

export interface UseAnswer {
  error: string | null;
  submit: (promise: Promise<Challenge>) => void;
}

export function useAnswer(onChallenge: (next: Challenge) => void): UseAnswer {
  const [error, setError] = useState<string | null>(null);

  const submit = (promise: Promise<Challenge>) => {
    promise
      .then((next) => {
        if (next.type === "error") {
          setError(next.error?.code ?? "unknown");
          return;
        }
        onChallenge(next);
      })
      .catch(() => setError("unknown"));
  };

  return { error, submit };
}
