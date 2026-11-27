import { useId } from "react";
import { Link } from "react-router";
import { useLingui } from "@lingui/react/macro";
import { Button } from "@bedrock/design";
import { isHttpError } from "../../api/http";
import { conditionOf } from "../../api/kube";
import { Ltr } from "../../components/Ltr";
import { StatusBadge } from "../../components/StatusBadge";
import { formatNumber } from "../../format";
import { useLocale } from "../../LocaleProvider";
import { listAt } from "../../resources/path";
import { definitionFor, refOf } from "../../resources/registry";
import { useLiveList } from "../../stream/live";
import styles from "./overview.module.css";

const items = [definitionFor("hosts")!, definitionFor("tenants")!, definitionFor("clusterimages")!, definitionFor("addons")!] as const;
const metricIcons = ["hosts", "tenants", "images", "addons"] as const;

const iconPaths = {
  hosts: "M3 2.5h10v4H3z M3 9.5h10v4H3z M5 4.5h.01 M5 11.5h.01",
  tenants: "M10 5a2 2 0 1 1-4 0 2 2 0 0 1 4 0z M4 13v-1a4 4 0 0 1 8 0v1 M11.5 3.3a2 2 0 0 1 0 3.4 M13.5 9.5a3 3 0 0 1 1 2.2V13",
  images: "M8 2 14 5.5 8 9 2 5.5 8 2z M2 8l6 3.5L14 8 M2 10.5 8 14l6-3.5",
  addons: "M6 1.5h4v4H6z M1.5 10.5h4v4h-4z M10.5 10.5h4v4h-4z M8 5.5V8 M3.5 10.5V8h9v2.5",
  arrow: "M3.5 8h9 M9 4.5 12.5 8 9 11.5",
};

function OverviewIcon({ icon, className }: { icon: keyof typeof iconPaths; className?: string }) {
  return (
    <svg aria-hidden="true" className={className} width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.25" strokeLinecap="round" strokeLinejoin="round">
      <path d={iconPaths[icon]} />
    </svg>
  );
}

type InventoryQuery = ReturnType<typeof useLiveList>;

function queryItems(query: InventoryQuery) {
  return query.isSuccess ? query.data.items : undefined;
}

function QueryState({ query }: { query: InventoryQuery }) {
  const { t } = useLingui();
  if (query.isPending) {
    return <span role="status">{t({ id: "common.state.loading", message: "Loading" })}</span>;
  }
  if (isHttpError(query.error) && query.error.status === 403) {
    return <span>{t({ id: "overview.restricted", message: "Access restricted" })}</span>;
  }
  return (
    <>
      <span role="status">{t({ id: "overview.unavailable", message: "Data unavailable" })}</span>
      <Button variant="secondary" disabled={query.isFetching} onClick={() => void query.refetch()}>{t({ id: "common.action.retry", message: "Retry" })}</Button>
    </>
  );
}

function InventoryCount({ def, query, icon }: { def: (typeof items)[number]; query: InventoryQuery; icon: (typeof metricIcons)[number] }) {
  const { i18n } = useLingui();
  const { locale } = useLocale();
  const id = useId();
  const data = queryItems(query);
  const top = (
    <span className={styles.metricTop}>
      <span className={styles.metricIcon}><OverviewIcon icon={icon} /></span>
      <span className={styles.metricLabel} id={`${id}-label`}>{i18n._(def.title)}</span>
    </span>
  );
  return (
    <li className={styles.metric}>
      {data ? (
        <Link className={styles.metricLink} to={`/admin/${def.path}`} aria-labelledby={`${id}-value ${id}-label`}>
          {top}
          <span className={styles.metricValue}>
            <span id={`${id}-value`}>{formatNumber(data.length, locale)}</span>
            <OverviewIcon icon="arrow" className={`${styles.metricArrow} br-icon--directional`} />
          </span>
        </Link>
      ) : (
        <div className={styles.metricState}>
          {top}
          {def.plural === "hosts" ? <span className={styles.metricValue} aria-hidden="true">—</span> : <QueryState query={query} />}
        </div>
      )}
    </li>
  );
}

function HostReadiness({ data }: { data: NonNullable<ReturnType<typeof queryItems>> }) {
  const { t } = useLingui();
  const { locale } = useLocale();
  const ready = data.filter((host) => conditionOf(host, "Ready")?.status === "True").length;
  const notReady = data.filter((host) => conditionOf(host, "Ready")?.status === "False").length;
  const readyLabel = t({ id: "status.condition.ready", message: "Ready" });
  const readiness = [
    { tone: "ready", count: ready, label: readyLabel },
    { tone: "notReady", count: notReady, label: t({ id: "status.condition.notReady", message: "Not ready" }) },
    { tone: "unknown", count: data.length - ready - notReady, label: t({ id: "status.condition.unknown", message: "Unknown" }) },
  ];
  return (
    <section className={styles.panel}>
      <header className={styles.panelHeader}>
        <h2>{t({ id: "overview.readiness", message: "Host readiness" })}</h2>
      </header>
      <div className={styles.panelBody}>
        {data.length > 0 ? (
          <div className={styles.readinessSummary}>
            <div>
              <strong className={styles.readinessValue}>
                <Ltr>{formatNumber(ready, locale)} / {formatNumber(data.length, locale)}</Ltr>
              </strong>{" "}{readyLabel}
            </div>
            <div className={styles.readinessBar} aria-hidden="true">
              {readiness.filter(({ count }) => count > 0).map(({ tone, count }) => (
                <span key={tone} data-tone={tone} style={{ flexGrow: count }} />
              ))}
            </div>
            <p>{t({ id: "overview.readinessDescription", message: "Reported readiness across registered hosts." })}</p>
          </div>
        ) : <p className={styles.empty}>{t({ id: "overview.noHosts", message: "No hosts registered." })}</p>}
        <dl className={styles.readinessLegend}>
          {readiness.map(({ tone, count, label }) => (
            <div key={tone} data-tone={tone}><dt>{label}</dt><dd>{formatNumber(count, locale)}</dd></div>
          ))}
        </dl>
      </div>
    </section>
  );
}

function HostSnapshot({ query }: { query: InventoryQuery }) {
  const { t } = useLingui();
  const titleId = useId();
  const data = queryItems(query);
  const hosts = data ? [...data].sort((left, right) => left.metadata.name.localeCompare(right.metadata.name)).slice(0, 4) : [];
  return (
    <div className={styles.panels}>
      <section className={styles.panel}>
        <header className={styles.panelHeader}>
          <h2 id={titleId}>{t({ id: "overview.hostList", message: "Hosts at a glance" })}</h2>
          {data ? <Link to="/admin/hosts">{t({ id: "overview.allHosts", message: "View all hosts" })}</Link> : null}
        </header>
        {hosts.length > 0 ? (
          <div className={`${styles.panelBody} ${styles.tableWrap}`} role="region" aria-labelledby={titleId} tabIndex={0}>
            <table className={styles.hostTable} aria-labelledby={titleId}>
              <thead>
                <tr>
                  <th scope="col">{t({ id: "col.name", message: "Name" })}</th>
                  <th scope="col">{t({ id: "col.roles", message: "Roles" })}</th>
                  <th scope="col">{t({ id: "col.status", message: "Status" })}</th>
                </tr>
              </thead>
              <tbody>
                {hosts.map((host) => (
                  <tr key={host.metadata.uid ?? host.metadata.name}>
                    <th scope="row">
                      <span className={styles.hostName}>
                        <span className={styles.hostIcon}><OverviewIcon icon="hosts" /></span>
                        <Link to={`/admin/hosts/${host.metadata.name}`}><Ltr>{host.metadata.name}</Ltr></Link>
                      </span>
                    </th>
                    <td><Ltr>{listAt(host, "spec.roles").join(" · ") || "—"}</Ltr></td>
                    <td><StatusBadge obj={host} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className={styles.panelBody}>
            {data ? <p className={styles.empty}>{t({ id: "overview.noHosts", message: "No hosts registered." })}</p> : (
              <div className={styles.empty}><QueryState query={query} /></div>
            )}
          </div>
        )}
      </section>
      {data ? <HostReadiness data={data} /> : null}
    </div>
  );
}

export default function OverviewPage() {
  const { t } = useLingui();
  const hosts = useLiveList(refOf(items[0]), {});
  const tenants = useLiveList(refOf(items[1]), {});
  const images = useLiveList(refOf(items[2]), {});
  const addons = useLiveList(refOf(items[3]), {});
  const summaryId = useId();
  const queries = [hosts, tenants, images, addons] as const;
  const updatesPaused = queries.some((query) => query.isSuccess && query.live === "paused");
  return (
    <div className={styles.overview}>
      <header className={styles.header}>
        <div className={styles.headerCopy}>
          <h1><bdi>{t({ id: "overview.title", message: "Infrastructure overview" })}</bdi></h1>
          <p>{t({ id: "overview.subtitle", message: "Monitor cluster inventory and see which hosts are ready." })}</p>
        </div>
        <div className={styles.headerActions}>
          <Link className="br-button br-button--primary" to="/admin/inventory">{t({ id: "nav.item.inventory", message: "Resource inventory" })}</Link>
        </div>
      </header>
      {updatesPaused ? <p role="status" className={styles.paused}>{t({ id: "overview.updatesPaused", message: "Some live updates are paused." })}</p> : null}
      <section className={styles.panel} aria-labelledby={summaryId}>
        <header className={styles.panelHeader}>
          <h2 id={summaryId}>{t({ id: "overview.resourceSummary", message: "Resource summary" })}</h2>
        </header>
        <div className={styles.panelBody}>
          <ul className={styles.metricGrid} role="list">
            {items.map((def, index) => <InventoryCount key={def.plural} def={def} query={queries[index]!} icon={metricIcons[index]!} />)}
          </ul>
        </div>
      </section>
      <HostSnapshot query={hosts} />
    </div>
  );
}
