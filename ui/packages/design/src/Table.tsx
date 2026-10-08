import type { ReactNode } from "react";

export function Table({ caption, children }: { caption: string; children: ReactNode }) {
  return (
    <table className="br-table">
      <caption className="br-table-caption">{caption}</caption>
      {children}
    </table>
  );
}

export function TableHead({ children }: { children: ReactNode }) {
  return <thead>{children}</thead>;
}

export function TableBody({ children }: { children: ReactNode }) {
  return <tbody>{children}</tbody>;
}

export function TableRow({ selected, children }: { selected?: boolean; children: ReactNode }) {
  return (
    <tr className="br-tr" aria-selected={selected}>
      {children}
    </tr>
  );
}

interface TableHeaderCellProps {
  sort?: "ascending" | "descending" | "none";
  onSort?: () => void;
  children: ReactNode;
}

export function TableHeaderCell({ sort, onSort, children }: TableHeaderCellProps) {
  return (
    <th className="br-th" scope="col" aria-sort={sort}>
      {onSort ? (
        <button type="button" className="br-th-button" onClick={onSort}>
          {children}
        </button>
      ) : (
        children
      )}
    </th>
  );
}

export function TableCell({ children }: { children?: ReactNode }) {
  return <td className="br-td">{children}</td>;
}
