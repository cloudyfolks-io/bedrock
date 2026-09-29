import { Menu } from "@base-ui/react/menu";

interface LanguageOption {
  value: string;
  label: string;
}

interface LanguageMenuProps {
  value: string;
  onChange: (value: string) => void;
  options: LanguageOption[];
}

export function LanguageMenu({ value, onChange, options }: LanguageMenuProps) {
  const current = options.find((option) => option.value === value);
  return (
    <Menu.Root>
      <Menu.Trigger className="br-language-menu-trigger">{current ? current.label : value}</Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner>
          <Menu.Popup className="br-language-menu-popup">
            {options.map((option) => (
              <Menu.Item key={option.value} className="br-language-menu-item" onClick={() => onChange(option.value)}>
                {option.label}
              </Menu.Item>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
