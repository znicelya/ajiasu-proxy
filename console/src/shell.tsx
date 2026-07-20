import { useEffect, useState } from "react";
import { NavLink, Route, Routes, useNavigate, useParams } from "react-router-dom";
import {
  Button, MessageBar, MessageBarBody, Spinner, Subtitle1, Tab, TabList, Text, Title1, tokens
} from "@fluentui/react-components";
import {
  AppsListDetail24Regular, Board24Regular, DataUsage24Regular, Database24Regular,
  DocumentBulletList24Regular, People24Regular, ShieldTask24Regular, Warning24Regular
} from "@fluentui/react-icons";
import { ApiError, getOverview, listResource, type ResourceKind, type ResourceRecord } from "./api";

const nav = [
  ["Overview", "overview", Board24Regular], ["Tenants", "tenants", Database24Regular],
  ["Members", "members", People24Regular], ["Accounts", "accounts", People24Regular],
  ["Pools", "pools", AppsListDetail24Regular], ["Endpoints", "endpoints", DataUsage24Regular],
  ["Operations", "operations", ShieldTask24Regular], ["Nodes", "nodes", Database24Regular],
  ["Health", "health", Warning24Regular], ["Quotas", "quotas", DataUsage24Regular],
  ["Audit", "audit", DocumentBulletList24Regular]
] as const;

export function App() {
  const tenantId = new URLSearchParams(window.location.search).get("tenant") ?? "current";
  return <Routes>
    <Route path="*" element={<Shell tenantId={tenantId} />} />
  </Routes>;
}

function Shell({ tenantId }: { tenantId: string }) {
  return <div className="app-shell">
    <a className="skip-link" href="#main-content">Skip to main content</a>
    <aside className="rail" aria-label="Primary navigation">
      <div className="brand-mark"><span className="brand-dot" /> AJiaSu</div>
      <div className="tenant-context"><Text size={200}>ACTIVE TENANT</Text><strong>{tenantId}</strong></div>
      <nav className="nav-list">{nav.map(([label, path, Icon]) => <NavLink key={path} to={`/${path}?tenant=${encodeURIComponent(tenantId)}`} className={({ isActive }) => isActive ? "nav-item active" : "nav-item"}><Icon /><span>{label}</span></NavLink>)}</nav>
      <div className="rail-footer"><Text size={200}>SECURE SESSION</Text><span className="session-state"><span className="status-dot good" /> Protected</span></div>
    </aside>
    <main className="content" id="main-content" tabIndex={-1}><header className="topbar"><div><Text className="eyebrow">CONTROL ROOM / {tenantId}</Text><Title1>Platform operations</Title1></div><Button appearance="subtle" onClick={() => window.location.assign("/api/v1/auth/logout")}>Sign out</Button></header><Routes>
      <Route path="/" element={<Overview tenantId={tenantId} />} />
      <Route path="/overview" element={<Overview tenantId={tenantId} />} />
      {nav.slice(1).map(([label, path]) => <Route key={path} path={`/${path}`} element={<ResourcePage tenantId={tenantId} kind={path as ResourceKind} label={label} />} />)}
    </Routes></main>
  </div>;
}

function Overview({ tenantId }: { tenantId: string }) {
  const [state, setState] = useState<{ loading: boolean; data?: Record<string, unknown>; error?: string }>({ loading: true });
  useEffect(() => { getOverview(tenantId).then((data) => setState({ loading: false, data })).catch((error: unknown) => setState({ loading: false, error: messageFor(error) })); }, [tenantId]);
  if (state.loading) return <LoadingState label="Loading platform posture" />;
  if (state.error) return <ErrorState message={state.error} />;
  const data = state.data ?? {};
  const metrics = ["nodes", "gateways", "fixed_assigned", "pool_assigned"].map((key) => ({ label: key.replaceAll("_", " "), value: String(data[key] ?? "—") }));
  return <section className="page-stack"><div className="page-intro"><div><Text className="eyebrow">TENANT POSTURE</Text><Subtitle1>At a glance</Subtitle1><Text block className="intro-copy">A calm view of capacity, health, and active control-plane work.</Text></div><span className="health-chip"><span className="status-dot good" /> Systems nominal</span></div><div className="metric-grid">{metrics.map((metric) => <div className="metric" key={metric.label}><Text className="metric-label">{metric.label}</Text><strong>{metric.value}</strong><Text size={200}>live control-plane view</Text></div>)}</div><div className="split-grid"><section className="section-block"><div className="section-heading"><Subtitle1>Operator focus</Subtitle1><Text size={200}>Signals that need a decision</Text></div><div className="focus-row"><Warning24Regular /><div><strong>Review quota headroom before scaling</strong><Text block size={200}>Keep account concurrency below the tenant ceiling while new assignments converge.</Text></div></div></section><section className="section-block"><div className="section-heading"><Subtitle1>Session contract</Subtitle1><Text size={200}>Requests remain tenant-scoped</Text></div><Text className="body-copy">Changes use version checks and idempotency keys. Secrets never enter this browser session or its local storage.</Text></section></div></section>;
}

function ResourcePage({ tenantId, kind, label }: { tenantId: string; kind: ResourceKind; label: string }) {
  const [state, setState] = useState<{ loading: boolean; rows: ResourceRecord[]; cursor?: string; error?: string }>({ loading: true, rows: [] });
  const load = (cursor?: string) => { setState((current) => ({ ...current, loading: true, error: undefined })); listResource(tenantId, kind, cursor).then((page) => setState({ loading: false, rows: page.items, cursor: page.next_cursor })).catch((error: unknown) => setState({ loading: false, rows: [], error: messageFor(error) })); };
  useEffect(() => { load(); }, [tenantId, kind]);
  return <section className="page-stack"><div className="page-intro"><div><Text className="eyebrow">TENANT RESOURCE</Text><Subtitle1>{label}</Subtitle1><Text block className="intro-copy">Tenant-scoped records with explicit state and safe concurrency.</Text></div><Button appearance="primary" onClick={() => load()}>Refresh</Button></div>{state.error ? <ErrorState message={state.error} /> : state.loading ? <LoadingState label={`Loading ${label.toLowerCase()}`} /> : state.rows.length === 0 ? <EmptyState label={label} /> : <ResourceTable rows={state.rows} />}{state.cursor && <Button appearance="subtle" onClick={() => load(state.cursor)}>Load next page</Button>}</section>;
}

function ResourceTable({ rows }: { rows: ResourceRecord[] }) {
  const keys = Array.from(new Set(rows.flatMap((row) => Object.keys(row).filter((key) => !key.includes("secret") && key !== "credential")))).slice(0, 5);
  return <div className="table-wrap"><table><thead><tr>{keys.map((key) => <th key={key}>{key.replaceAll("_", " ")}</th>)}</tr></thead><tbody>{rows.map((row, index) => <tr key={String(row.id ?? index)}>{keys.map((key) => <td key={key}>{String(row[key] ?? "—")}</td>)}</tr>)}</tbody></table></div>;
}

function LoadingState({ label }: { label: string }) { return <div className="state-panel"><Spinner label={label} /></div>; }
function EmptyState({ label }: { label: string }) { return <div className="state-panel"><Subtitle1>No {label.toLowerCase()} yet</Subtitle1><Text block>Create the first record through the protected management API.</Text></div>; }
function ErrorState({ message }: { message: string }) { const navigate = useNavigate(); return <MessageBar intent="error"><MessageBarBody>{message} <Button appearance="transparent" onClick={() => navigate("/overview")}>Return to overview</Button></MessageBarBody></MessageBar>; }
function messageFor(error: unknown): string { if (error instanceof ApiError && error.status === 401) return "Your secure session expired. Sign in again to continue."; return error instanceof Error ? error.message : "The control plane could not be reached."; }

export { Tab, TabList, tokens };
