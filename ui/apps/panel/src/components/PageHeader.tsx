import type { ReactNode } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import styles from "./PageHeader.module.css";

export interface Crumb {
  label: string;
  to?: string;
}

interface PageHeaderProps {
  title: string;
  description?: string;
  breadcrumbs?: Crumb[];
  actions?: ReactNode;
}

export function PageHeader({ title, description, breadcrumbs, actions }: PageHeaderProps) {
  const { t } = useLingui();
  return (
    <header className={`br-page-header ${styles.header}`}>
      {breadcrumbs && breadcrumbs.length > 0 ? (
        <nav aria-label={t({ id: "shell.breadcrumb.label", message: "Breadcrumb" })}>
          <ol className="br-breadcrumb">
            {breadcrumbs.map((crumb, index) => (
              <li key={`${crumb.label}-${index}`} aria-current={index === breadcrumbs.length - 1 ? "page" : undefined}>
                {index > 0 ? (
                  <span aria-hidden="true" className="br-icon br-icon--directional br-breadcrumb-separator">
                    ›
                  </span>
                ) : null}
                {crumb.to ? <Link to={crumb.to}><bdi>{crumb.label}</bdi></Link> : <bdi>{crumb.label}</bdi>}
              </li>
            ))}
          </ol>
        </nav>
      ) : null}
      <div className="br-page-title-row">
        <div className={styles.title}>
          <h1 className="br-page-title"><bdi>{title}</bdi></h1>
          {description ? <p>{description}</p> : null}
        </div>
        {actions ? <div className="br-page-actions">{actions}</div> : null}
      </div>
    </header>
  );
}
