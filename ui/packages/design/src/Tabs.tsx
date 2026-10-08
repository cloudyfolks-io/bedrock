import type { ReactNode } from "react";
import { Tabs as BaseTabs } from "@base-ui/react/tabs";

interface TabsProps {
  value: string;
  onValueChange: (value: string) => void;
  children: ReactNode;
}

export function Tabs({ value, onValueChange, children }: TabsProps) {
  return (
    <BaseTabs.Root value={value} onValueChange={(next) => onValueChange(String(next))}>
      {children}
    </BaseTabs.Root>
  );
}

export function TabList({ children }: { children: ReactNode }) {
  return <BaseTabs.List className="br-tab-list">{children}</BaseTabs.List>;
}

export function Tab({ value, children, disabled }: { value: string; children: ReactNode; disabled?: boolean | undefined }) {
  return (
    <BaseTabs.Tab value={value} className="br-tab" disabled={disabled}>
      {children}
    </BaseTabs.Tab>
  );
}

export function TabPanel({ value, children }: { value: string; children: ReactNode }) {
  return (
    <BaseTabs.Panel value={value} className="br-tab-panel">
      {children}
    </BaseTabs.Panel>
  );
}
