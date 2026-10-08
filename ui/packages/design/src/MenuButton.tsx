import { Menu } from "@base-ui/react/menu";

interface MenuItem {
  id: string;
  label: string;
  onSelect: () => void;
  disabled?: boolean;
  tone?: "danger" | "neutral";
}

export function MenuButton({ label, items }: { label: string; items: MenuItem[] }) {
  return (
    <Menu.Root>
      <Menu.Trigger className="br-button br-button--secondary" aria-label={label}>
        <span aria-hidden="true">⋯</span>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner>
          <Menu.Popup className="br-menu-popup">
            {items.map((item) => (
              <Menu.Item
                key={item.id}
                className="br-menu-item"
                data-tone={item.tone ?? "neutral"}
                disabled={item.disabled}
                onClick={item.onSelect}
              >
                {item.label}
              </Menu.Item>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
