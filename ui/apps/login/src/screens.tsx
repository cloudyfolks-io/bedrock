import type { ComponentType } from "react";
import type { Challenge } from "./types";

export interface ScreenProps {
  challenge: Challenge;
  onChallenge: (next: Challenge) => void;
}

function Placeholder({ challenge }: ScreenProps) {
  return <pre>{JSON.stringify(challenge)}</pre>;
}

export function screenFor(challenge: Challenge): ComponentType<ScreenProps> {
  return Placeholder;
}
