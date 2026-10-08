import { Fragment, type ReactNode } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";

export interface Crumb {
  label: string;
  to?: string;
}

interface PageHeaderProps {
  title: string;
  breadcrumbs?: Crumb[];
  actions?: ReactNode;
}

export function PageHeader({ title, breadcrumbs, actions }: PageHeaderProps) {
  const { t } = useLingui();
  return (
    <header className="br-page-header">
      {breadcrumbs && breadcrumbs.length > 0 ? (
        <nav aria-label={t({ id: "shell.breadcrumb.label", message: "Breadcrumb" })}>
          <ol className="br-breadcrumb">
            {breadcrumbs.map((crumb, index) => (
              <Fragment key={`${crumb.label}-${index}`}>
                {index > 0 ? (
                  <li aria-hidden="true" className="br-icon br-icon--directional br-breadcrumb-separator">
                    ›
                  </li>
                ) : null}
                <li>{crumb.to ? <Link to={crumb.to}>{crumb.label}</Link> : crumb.label}</li>
              </Fragment>
            ))}
          </ol>
        </nav>
      ) : null}
      <div className="br-page-title-row">
        <h1 className="br-page-title">{title}</h1>
        {actions ? <div className="br-page-actions">{actions}</div> : null}
      </div>
    </header>
  );
}
