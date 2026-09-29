import type { ReactNode } from "react";
import { Button as BaseButton } from "@base-ui/react/button";

interface ButtonProps {
  variant: "primary" | "secondary";
  type?: "button" | "submit";
  disabled?: boolean;
  onClick?: () => void;
  children: ReactNode;
}

export function Button({ variant, type = "button", disabled, onClick, children }: ButtonProps) {
  return (
    <BaseButton type={type} disabled={disabled} onClick={onClick} className={`br-button br-button--${variant}`}>
      {children}
    </BaseButton>
  );
}
