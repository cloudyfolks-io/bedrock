import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { ThemeProvider } from "@bedrock/design";
import { readStored, writeStored } from "./storage";

export type ThemeChoice = "system" | "light" | "dark";

interface ThemeChoiceValue {
  choice: ThemeChoice;
  setChoice: (choice: ThemeChoice) => void;
}

const ThemeChoiceContext = createContext<ThemeChoiceValue | null>(null);

function isChoice(value: string | null): value is ThemeChoice {
  return value === "system" || value === "light" || value === "dark";
}

export function ThemeChoiceProvider({ children }: { children: ReactNode }) {
  const [choice, setChoiceState] = useState<ThemeChoice>(() => {
    const stored = readStored("theme");
    return isChoice(stored) ? stored : "system";
  });
  const setChoice = useCallback((next: ThemeChoice) => {
    writeStored("theme", next);
    setChoiceState(next);
  }, []);
  const value = useMemo(() => ({ choice, setChoice }), [choice, setChoice]);
  return (
    <ThemeChoiceContext.Provider value={value}>
      {choice === "system" ? <ThemeProvider>{children}</ThemeProvider> : <ThemeProvider theme={choice}>{children}</ThemeProvider>}
    </ThemeChoiceContext.Provider>
  );
}

export function useThemeChoice(): ThemeChoiceValue {
  const value = useContext(ThemeChoiceContext);
  if (!value) {
    throw new Error("useThemeChoice needs a ThemeChoiceProvider");
  }
  return value;
}
