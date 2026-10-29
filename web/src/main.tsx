import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowUpRight,
  Box,
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  Database,
  Globe,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Plus,
  RefreshCw,
  Search,
  Settings,
  ShieldCheck,
  Terminal,
  Wallet,
  X,
  Zap,
} from "lucide-react";
import { messages, resolveLanguage } from "./i18n.mjs";
import "./style.css";
type Row = Record<string, any>;
type Page =
  | "overview"
  | "organizations"
  | "projects"
  | "upstream_keys"
  | "gateway_keys"
  | "logs"
  | "costs"
  | "settings"
  | "audit";
let csrf = "";
async function api(path: string, options: RequestInit = {}) {
  let res: Response;
  try {
    res = await fetch("/admin/api/" + path, {
      ...options,
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
        ...options.headers,
      },
    });
  } catch {
    throw Error("network_error");
  }
  const token = res.headers.get("X-CSRF-Token");
  if (token) csrf = token;
  const v = await res.json().catch(() => ({}));
  if (!res.ok) throw Error(v.error?.code || "unexpected_error");
  return v;
}
function App() {
  const [preference, setPreference] = useState(
      localStorage.getItem("gyai-language") || "auto",
    ),
    [browser, setBrowser] = useState(navigator.languages as readonly string[]);
  const language = resolveLanguage(preference, [...browser]) as "en" | "zh",
    locale = language === "zh" ? "zh-CN" : "en-US",
    t = (k: string) => (messages[language] as Record<string, string>)[k] || k;
  const [session, setSession] = useState<string | null | undefined>(),
    [page, setPage] = useState<Page>("overview"),
    [data, setData] = useState<Row[]>([]),
    [dashboard, setDashboard] = useState<Row>({}),
    [settings, setSettings] = useState<Row>({});
  const [orgs, setOrgs] = useState<Row[]>([]),
    [upstreamProjects, setUpstreamProjects] = useState<Row[]>([]),
    [keys, setKeys] = useState<Row[]>([]),
    [models, setModels] = useState<Row[]>([]),
    [official, setOfficial] = useState<Row[]>([]);
  const [loading, setLoading] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [modal, setModal] = useState<Row | null>(null),
    [secret, setSecret] = useState(""),
    [copied, setCopied] = useState(false),
    [offset, setOffset] = useState(0),
    [query, setQuery] = useState("");
  const [org, setOrg] = useState(""),
    [project, setProject] = useState(""),
    [key, setKey] = useState("");
  useEffect(() => {
    const fn = () => setBrowser(navigator.languages);
    addEventListener("languagechange", fn);
    return () => removeEventListener("languagechange", fn);
  }, []);
  useEffect(() => {
    document.documentElement.lang = locale;
    document.title = `${t("brand")} · ${t("product")}`;
    localStorage.setItem("gyai-language", preference);
  }, [preference, language]);
  function fail(e: unknown) {
    const code = e instanceof Error ? e.message : "unexpected_error";
    setError(code);
    if (code === "unauthorized") setSession(null);
  }
  useEffect(() => {
    api("session")
      .then((v) => {
        csrf = v.csrf;
        setSession(v.username);
      })
      .catch(() => setSession(null));
  }, []);
  async function load() {
    setLoading(true);
    setError("");
    try {
      const [o, p, k, m] = await Promise.all(
        ["organizations", "upstream_projects", "upstream_keys", "models"].map(
          (p) => api(p + "?limit=500"),
        ),
      );
      setOrgs(o.data);
      setUpstreamProjects(p.data);
      setKeys(k.data);
      setModels(m.data);
      if (page === "overview") setDashboard(await api("dashboard"));
      else if (page === "settings") setSettings(await api("settings"));
      else {
        const q = new URLSearchParams({ limit: "100", offset: String(offset) });
        if (page === "costs") {
          q.set("org_id", org);
          q.set("project_id", project);
          q.set("api_key_id", key);
        }
        setData((await api(page + "?" + q)).data);
      }
    } catch (e) {
      fail(e);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    if (session) void load();
  }, [session, page, offset, org, project, key]);
  async function login(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const v = await api("login", {
        method: "POST",
        body: JSON.stringify(Object.fromEntries(new FormData(e.currentTarget))),
      });
      csrf = v.csrf;
      setSession(v.username);
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  }
  async function action(id: string, name: string) {
    setBusy(true);
    setError("");
    try {
      await api(`${page}/${id}/${name}`, { method: "POST", body: "{}" });
      setNotice("successful");
      await load();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  }
  async function save(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!modal) return;
    const input: Row = Object.fromEntries(new FormData(e.currentTarget));
    input.enabled = input.enabled === "true";
    if (page === "gateway_keys") {
      input.models = input.models
        .split(",")
        .map((s: string) => s.trim())
        .filter(Boolean);
      input.cidrs = input.cidrs
        .split(/[\n,]/)
        .map((s: string) => s.trim())
        .filter(Boolean);
      input.expires_at = input.expires_at
        ? new Date(input.expires_at).toISOString()
        : "";
    }
    setBusy(true);
    setError("");
    try {
      const v = await api(page + (modal.id ? "/" + modal.id : ""), {
        method: modal.id ? "PATCH" : "POST",
        body: JSON.stringify(input),
      });
      setModal(null);
      if (v.key) {
        setSecret(v.key);
        setCopied(false);
      }
      setNotice(v.warning || "updated");
      await load();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  }
  function navigate(p: Page) {
    setPage(p);
    setOffset(0);
    setQuery("");
    setError("");
    setNotice("");
  }
  const number = (v: any) =>
    new Intl.NumberFormat(locale, { maximumFractionDigits: 4 }).format(
      Number(v || 0),
    );
  const date = (v: any) =>
    v
      ? new Intl.DateTimeFormat(locale, {
          dateStyle: "medium",
          timeStyle: "short",
        }).format(new Date(v))
      : "—";
  const languages = (
    <label className="language">
      <Globe size={15} />
      <select
        aria-label="Language / 语言"
        value={preference}
        onChange={(e) => setPreference(e.target.value)}
      >
        <option value="auto">{t("auto")}</option>
        <option value="zh">简体中文</option>
        <option value="en">English</option>
      </select>
    </label>
  );
  const alert = error && (
    <div className="alert" role="alert">
      {(messages[language] as Record<string, string>)[error] ||
        t("unexpected_error")}
      <button onClick={() => setError("")}>
        <X size={16} />
      </button>
    </div>
  );
  const mark = (
    <span className="wordmark">
      <span className="logo">
        <Zap size={23} fill="currentColor" />
      </span>
      <span>
        {t("brand")}
        <small>{t("product")}</small>
      </span>
    </span>
  );
  if (session === undefined)
    return (
      <div className="boot">
        <Zap />
        {t("loading")}
      </div>
    );
  if (session === null)
    return (
      <div className="login-layout">
        <aside className="login-art">
          {mark}
          <div>
            <span className="eyebrow">GUANGYU / AI INFRASTRUCTURE</span>
            <h1>
              {language === "zh" ? (
                <>
                  连接智能，
                  <br />
                  由你掌控。
                </>
              ) : (
                <>
                  Intelligence,
                  <br />
                  on your terms.
                </>
              )}
            </h1>
            <p>{t("subtitle")}</p>
            <div className="network-art">
              <div className="node">API</div>
              <div className="connection" />
              <div className="node hub">
                <Zap size={30} />
              </div>
              <div className="connection" />
              <div className="node">AI</div>
            </div>
          </div>
          <span className="security-line">
            <ShieldCheck size={16} />
            {t("secure")}
          </span>
        </aside>
        <main className="login-main">
          <div className="login-language">{languages}</div>
          <form className="login-card" onSubmit={login}>
            <span className="eyebrow">{t("console")}</span>
            <h2>{t("loginTitle")}</h2>
            <p>{t("loginSubtitle")}</p>
            {alert}
            <label>
              {t("username")}
              <input
                name="username"
                required
                autoComplete="username"
                autoFocus
              />
            </label>
            <label>
              {t("password")}
              <input
                name="password"
                type="password"
                required
                autoComplete="current-password"
              />
            </label>
            <button className="primary" disabled={busy}>
              {t(busy ? "loading" : "login")}
              <ArrowUpRight size={17} />
            </button>
          </form>
        </main>
      </div>
    );
  const nav: [Page, React.ReactNode][] = [
    ["overview", <LayoutDashboard size={18} />],
    ["organizations", <Globe size={18} />],
    ["projects", <Box size={18} />],
    ["upstream_keys", <KeyRound size={18} />],
    ["gateway_keys", <ShieldCheck size={18} />],
    ["logs", <Activity size={18} />],
    ["costs", <Wallet size={18} />],
    ["audit", <Terminal size={18} />],
    ["settings", <Settings size={18} />],
  ];
  const canAdd = [
    "organizations",
    "projects",
    "upstream_keys",
    "gateway_keys",
  ].includes(page);
  const status = (r: Row) =>
    !r.enabled ||
    r.upstream_project_enabled === false ||
    r.org_enabled === false
      ? "disabled"
      : r.invalid
        ? "invalid"
        : r.quota_blocked ||
            r.upstream_project_quota_blocked ||
            r.org_quota_blocked
          ? "blocked"
          : page === "upstream_keys" && !r.model_synced_at
            ? "pending"
            : "active";
  const columns: Record<string, string[]> = {
    organizations: [
      "name",
      "admin_status",
      "admin_checked_at",
      "projects_access",
      "costs_access",
      "status",
      "probe_model",
      "next_probe_at",
      "synced_at",
      "sync_error",
    ],
    projects: ["name", "description", "status", "created_at"],
    upstream_keys: [
      "name",
      "org_name",
      "upstream_project_id",
      "models",
      "status",
      "model_synced_at",
      "next_probe_at",
      "upstream_api_key_id",
    ],
    gateway_keys: ["name", "models", "cidrs", "status", "expires_at"],
    logs: [
      "created_at",
      "gateway_name",
      "upstream_name",
      "model",
      "endpoint",
      "ip",
      "status",
      "duration_ms",
      "input_tokens",
      "output_tokens",
      "error_code",
      "kind",
    ],
    costs: [
      "start_time",
      "org_name",
      "project_name",
      "upstream_name",
      "kind",
      "model",
      "amount",
      "currency",
      "input_tokens",
      "output_tokens",
      "num_requests",
    ],
    audit: ["created_at", "action", "resource", "resource_id"],
  };
  function cell(c: string, r: Row) {
    const v = r[c];
    if (c === "admin_status") {
      return (
        <span
          className={
            "badge " +
            (v === "valid"
              ? "active"
              : v === "unchecked"
                ? "pending"
                : "invalid")
          }
        >
          <i />
          {t("admin_" + v)}
        </span>
      );
    }
    if (c === "projects_access" || c === "costs_access") {
      return t("access_" + v);
    }
    if (c === "status")
      return page === "logs" ? (
        <span className={`http-status ${v < 400 ? "success" : "failed"}`}>
          {v}
        </span>
      ) : (
        <span className={"badge " + status(r)}>
          <i />
          {t(status(r))}
        </span>
      );
    if (
      [
        "created_at",
        "expires_at",
        "model_synced_at",
        "synced_at",
        "next_probe_at",
        "admin_checked_at",
      ].includes(c)
    )
      return c === "expires_at" && !v ? t("never") : date(v);
    if (c === "start_time") return date(v * 1000);
    if (c === "models" || c === "cidrs")
      return (
        <div className="model-tags">
          {(v || []).slice(0, 3).map((s: string) => (
            <code key={s}>{s}</code>
          ))}
          {v?.length > 3 && <small>+{v.length - 3}</small>}
        </div>
      );
    if (c === "duration_ms") return number(v) + " ms";
    if (["input_tokens", "output_tokens", "num_requests", "amount"].includes(c))
      return number(v);
    if (c === "sync_error" || c === "admin_error") return v ? t(v) : "—";
    if (c === "kind") return t(v === "probe" ? "probeKind" : v);
    if (c === "name")
      return (
        <div className="resource-name">
          <strong>{v}</strong>
          <small>{r.prefix ? r.prefix + "…" : r.upstream_id || ""}</small>
        </div>
      );
    return v || "—";
  }
  const field = (n: string, type = "text", hint = "", required = false) => (
    <label key={n}>
      {t(n)}
      <input
        name={n}
        type={type}
        required={required}
        defaultValue={modal?.[n] || ""}
        autoComplete={type === "password" ? "new-password" : "off"}
      />
      {hint && <small>{t(hint)}</small>}
    </label>
  );
  const select = (n: string, rows: Row[]) => (
    <label key={n}>
      {t(n)}
      <select name={n} required defaultValue={modal?.[n] || ""}>
        <option value="">{t("select")}</option>
        {rows.map((r) => (
          <option key={r.id} value={r.id}>
            {r.name}
          </option>
        ))}
      </select>
    </label>
  );
  return (
    <div className="app">
      <aside className="sidebar">
        <button className="brand-button" onClick={() => navigate("overview")}>
          {mark}
        </button>
        <div className="sidebar-label">{t("workspace")}</div>
        <nav>
          {nav.map(([p, icon], i) => (
            <React.Fragment key={p}>
              {i === 7 && (
                <div className="sidebar-label administration">
                  {t("administration")}
                </div>
              )}
              <button
                className={page === p ? "selected" : ""}
                onClick={() => navigate(p)}
              >
                {icon}
                <span>{t(p)}</span>
                {page === p && <i className="nav-dot" />}
              </button>
            </React.Fragment>
          ))}
        </nav>
        <div className="sidebar-footer">
          <div className="health">
            <i />
            {t("health")}
          </div>
          <div className="profile">
            <div className="avatar">{session[0].toUpperCase()}</div>
            <div>
              <strong>{session}</strong>
              <small>{t("administration")}</small>
            </div>
            <button
              title={t("logout")}
              onClick={async () => {
                try {
                  await api("logout", { method: "POST" });
                  setSession(null);
                } catch (e) {
                  fail(e);
                }
              }}
            >
              <LogOut size={17} />
            </button>
          </div>
        </div>
      </aside>
      <div className="main-shell">
        <header className="topbar">
          <span>
            {t("workspace")}
            <span className="slash">/</span>
            <strong>{t(page)}</strong>
          </span>
          {languages}
        </header>
        <main className="main">
          <div className="page-heading">
            <div>
              <span className="eyebrow">
                {page === "overview" ? "GUANGYU GATEWAY" : t("console")}
              </span>
              <h1>{t(page)}</h1>
              <p>
                {t(
                  page === "overview"
                    ? "introduction"
                    : page === "costs"
                      ? "statsHint"
                      : page === "settings"
                        ? "settingsHint"
                        : page === "projects"
                          ? "projectPoolHint"
                          : "subtitle",
                )}
              </p>
            </div>
            <div className="heading-actions">
              <button
                className="secondary"
                disabled={loading || busy}
                onClick={() => void load()}
              >
                <RefreshCw size={16} className={loading ? "spinning" : ""} />
                {t("refresh")}
              </button>
              {canAdd && (
                <button
                  className="primary"
                  onClick={() => {
                    setModal({});
                    setOfficial([]);
                    setError("");
                  }}
                >
                  <Plus size={17} />
                  {t("add")}
                </button>
              )}
            </div>
          </div>
          {alert}
          {notice && (
            <div className="notice">
              <Check size={16} />
              {t(notice)}
              <button onClick={() => setNotice("")}>
                <X size={16} />
              </button>
            </div>
          )}
          {page === "overview" ? (
            <>
              <div className="stats-grid">
                {[
                  ["available", dashboard.available_keys, KeyRound],
                  ["requests", dashboard.requests_today, Activity],
                  [
                    "tokens",
                    Number(dashboard.input_tokens || 0) +
                      Number(dashboard.output_tokens || 0),
                    Zap,
                  ],
                  ["blockedScopes", dashboard.blocked_scopes, ShieldCheck],
                ].map(([label, value, Icon]: any) => (
                  <section className="stat-card" key={label}>
                    <div className="stat-label">
                      {t(label)}
                      <Icon size={18} />
                    </div>
                    <strong>{number(value)}</strong>
                    <small>
                      {label === "available"
                        ? t("total") + " " + number(dashboard.upstream_keys)
                        : label === "tokens"
                          ? t("input_tokens") +
                            " " +
                            number(dashboard.input_tokens)
                          : "—"}
                    </small>
                  </section>
                ))}
              </div>
              <div className="overview-grid">
                <section className="panel">
                  <div className="panel-heading">
                    <h2>{t("infrastructure")}</h2>
                    <span className="muted">{t("total")}</span>
                  </div>
                  {[
                    ["organizations", Globe, dashboard.organizations],
                    ["projects", Box, dashboard.projects],
                    ["gateway_keys", KeyRound, dashboard.gateway_keys],
                  ].map(([label, Icon, value]: any) => (
                    <button
                      key={label}
                      className="infrastructure-row"
                      onClick={() => navigate(label)}
                    >
                      <div className="resource-icon">
                        <Icon size={20} />
                      </div>
                      <span>{t(label)}</span>
                      <strong>{number(value)}</strong>
                      <ChevronRight size={17} />
                    </button>
                  ))}
                </section>
                <section className="panel balance-panel">
                  <div className="panel-heading">
                    <h2>{t("exactBalance")}</h2>
                    <Wallet size={18} />
                  </div>
                  <strong className="unknown-balance">
                    {t("unknown")}
                    <span>—</span>
                  </strong>
                  <p>{t("balanceHint")}</p>
                  <button
                    className="text-button"
                    onClick={() => navigate("costs")}
                  >
                    {t("costs")}
                    <ArrowUpRight size={16} />
                  </button>
                </section>
              </div>
              <div className="connection-panel">
                <div className="resource-icon">
                  <Terminal size={23} />
                </div>
                <div>
                  <span className="eyebrow">{t("apiEndpoint")}</span>
                  <code>{location.origin}/v1</code>
                </div>
                <span className="openai-chip">OPENAI COMPATIBLE</span>
              </div>
              {!dashboard.upstream_keys && (
                <section className="setup-panel">
                  <h3>{t("setup")}</h3>
                  <p>{t("setupHint")}</p>
                  <button
                    className="primary"
                    onClick={() => navigate("organizations")}
                  >
                    {t("add")} {t("organizations")}
                    <ArrowUpRight size={15} />
                  </button>
                </section>
              )}
            </>
          ) : page === "settings" ? (
            <>
              <section className="panel">
                <div className="panel-heading">
                  <h2>{t("deploymentSettings")}</h2>
                  <Settings size={18} />
                </div>
                <div className="settings-list">
                  {Object.entries(settings)
                    .filter(([k]) => k !== "balance")
                    .map(([k, v]) => (
                      <div key={k}>
                        <span>{t(k)}</span>
                        <code>
                          {typeof v === "boolean"
                            ? t(v ? "on" : "off")
                            : Array.isArray(v)
                              ? v.join(", ") || "—"
                              : String(v)}
                        </code>
                      </div>
                    ))}
                </div>
              </section>
              <section className="privacy">
                <ShieldCheck size={24} />
                <div>
                  <h3>{t("security")}</h3>
                  <p>{t("securityHint")}</p>
                </div>
              </section>
            </>
          ) : (
            <section className="panel data-panel">
              <div className="table-toolbar">
                <label className="search">
                  <Search size={16} />
                  <input
                    aria-label={t("name")}
                    placeholder={t("name") + " / ID"}
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                  />
                </label>
                {page === "costs" && (
                  <div className="filters">
                    <select
                      value={org}
                      onChange={(e) => {
                        setOrg(e.target.value);
                        setProject("");
                        setKey("");
                        setOffset(0);
                      }}
                    >
                      <option value="">{t("filter")}</option>
                      {orgs.map((o) => (
                        <option key={o.id} value={o.id}>
                          {o.name}
                        </option>
                      ))}
                    </select>
                    <select
                      value={project}
                      onChange={(e) => {
                        setProject(e.target.value);
                        setKey("");
                        setOffset(0);
                      }}
                    >
                      <option value="">{t("allUpstreamProjects")}</option>
                      {upstreamProjects
                        .filter((p) => !org || p.org_id === org)
                        .map((p) => (
                          <option key={p.id} value={p.upstream_id}>
                            {p.name}
                          </option>
                        ))}
                    </select>
                    <select
                      value={key}
                      onChange={(e) => {
                        setKey(e.target.value);
                        setOffset(0);
                      }}
                    >
                      <option value="">{t("allKeys")}</option>
                      {keys
                        .filter(
                          (k) =>
                            k.upstream_api_key_id &&
                            (!org || k.org_id === org) &&
                            (!project || k.upstream_project_id === project),
                        )
                        .map((k) => (
                          <option key={k.id} value={k.upstream_api_key_id}>
                            {k.name}
                          </option>
                        ))}
                    </select>
                  </div>
                )}
                <span className="record-count">
                  {number(data.length)} {t("total")}
                </span>
              </div>
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      {columns[page]?.map((c) => (
                        <th key={c}>{t(c)}</th>
                      ))}
                      {canAdd && <th>{t("actions")}</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {data
                      .filter(
                        (r) =>
                          !query ||
                          JSON.stringify(r)
                            .toLowerCase()
                            .includes(query.toLowerCase()),
                      )
                      .map((r, i) => (
                        <tr key={r.id || i}>
                          {columns[page]?.map((c) => (
                            <td key={c}>{cell(c, r)}</td>
                          ))}
                          {canAdd && (
                            <td>
                              <div className="row-actions">
                                <button
                                  onClick={() => {
                                    setModal(r);
                                    setOfficial([]);
                                    setError("");
                                  }}
                                >
                                  {t("edit")}
                                </button>
                                {page === "upstream_keys" && (
                                  <>
                                    <button
                                      disabled={busy}
                                      onClick={() => action(r.id, "refresh")}
                                    >
                                      {t("refresh")}
                                    </button>
                                    {r.invalid && (
                                      <button
                                        disabled={busy}
                                        onClick={() =>
                                          action(r.id, "reset_auth")
                                        }
                                      >
                                        {t("reset_auth")}
                                      </button>
                                    )}
                                  </>
                                )}
                                {page === "organizations" && (
                                  <>
                                    <button
                                      disabled={busy || !r.has_admin_key}
                                      onClick={() => action(r.id, "verify")}
                                    >
                                      {t("verifyAdmin")}
                                    </button>
                                    <button
                                      disabled={busy || !r.has_admin_key}
                                      onClick={() => action(r.id, "sync")}
                                    >
                                      {t("sync")}
                                    </button>
                                  </>
                                )}
                                {r.quota_blocked && (
                                  <button
                                    disabled={busy}
                                    onClick={() => action(r.id, "probe")}
                                  >
                                    {t("probe")}
                                  </button>
                                )}
                              </div>
                              {r.model_sync_error && (
                                <small className="error-hint">
                                  {t(r.model_sync_error)}
                                </small>
                              )}
                              {r.admin_error && (
                                <small className="error-hint">
                                  {t(r.admin_error)}
                                </small>
                              )}
                              {r.last_error && (
                                <small className="error-hint">
                                  {r.last_error}
                                </small>
                              )}
                            </td>
                          )}
                        </tr>
                      ))}
                  </tbody>
                </table>
                {!data.length && (
                  <div className="empty-state">
                    <Database size={28} />
                    <h3>{t(loading ? "loading" : "empty")}</h3>
                    <p>{t("emptyHint")}</p>
                  </div>
                )}
              </div>
              <footer className="table-footer">
                <span>
                  {t("page")} {number(offset / 100 + 1)}
                </span>
                <div>
                  <button
                    disabled={!offset || loading}
                    onClick={() => setOffset(offset - 100)}
                  >
                    <ChevronLeft size={16} />
                    {t("previous")}
                  </button>
                  <button
                    disabled={data.length < 100 || loading}
                    onClick={() => setOffset(offset + 100)}
                  >
                    {t("next")}
                    <ChevronRight size={16} />
                  </button>
                </div>
              </footer>
            </section>
          )}
          <footer className="main-footer">
            <span>
              © {new Date().getFullYear()} {t("brand")} · {t("product")}
            </span>
            <span>
              <ShieldCheck size={13} />
              {t("secure")}
            </span>
          </footer>
        </main>
      </div>
      {modal && (
        <div className="modal-overlay">
          <section
            className="modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="modal-title"
          >
            <div className="modal-heading">
              <div>
                <span className="eyebrow">{t(page)}</span>
                <h2 id="modal-title">
                  {t(modal.id ? "edit" : "add")} {t(page)}
                </h2>
              </div>
              <button aria-label={t("close")} onClick={() => setModal(null)}>
                <X size={20} />
              </button>
            </div>
            <form onSubmit={save}>
              {alert}
              <div className="form-fields">
                {field("name", "text", "", !modal.id)}
                {page === "organizations" && (
                  <>
                    {!modal.id && field("upstream_id", "text", "", true)}
                    {field("admin_key", "password", "adminHint", !modal.id)}
                    {field("probe_model", "text", "probeHint")}
                  </>
                )}
                {page === "projects" && (
                  <>{field("description", "text", "projectPoolHint")}</>
                )}
                {page === "upstream_keys" && (
                  <>
                    {!modal.id && (
                      <>
                        {select("org_id", orgs)}
                        {field("key", "password", "upstreamHint", true)}
                      </>
                    )}
                    {field(
                      "upstream_project_id",
                      "text",
                      "upstreamProjectHint",
                    )}
                    <label>
                      {t("upstream_api_key_id")}
                      <input
                        name="upstream_api_key_id"
                        defaultValue={modal.upstream_api_key_id || ""}
                        list="official-keys"
                      />
                      <datalist id="official-keys">
                        {official.map((k) => (
                          <option key={k.id} value={k.id}>
                            {k.name} · {k.redacted_value}
                          </option>
                        ))}
                      </datalist>
                      <small>{t("officialHint")}</small>
                    </label>
                    <button
                      className="secondary"
                      type="button"
                      disabled={busy}
                      onClick={async (e) => {
                        const form = e.currentTarget.closest("form")!;
                        const pid =
                          modal.org_id || new FormData(form).get("org_id");
                        if (!pid) {
                          setError("upstream_key_required");
                          return;
                        }
                        setBusy(true);
                        try {
                          setOfficial(
                            (await api(`organizations/${pid}/official_keys`))
                              .data,
                          );
                        } catch (e) {
                          fail(e);
                        } finally {
                          setBusy(false);
                        }
                      }}
                    >
                      {t("officialKeys")}
                      <RefreshCw size={14} />
                    </button>
                    {official.length > 0 && (
                      <div className="official-list">
                        {official.map((k) => (
                          <button
                            key={k.id}
                            type="button"
                            onClick={(e) => {
                              (
                                e.currentTarget
                                  .closest("form")!
                                  .elements.namedItem(
                                    "upstream_api_key_id",
                                  ) as HTMLInputElement
                              ).value = k.id;
                              const form = e.currentTarget.closest("form")!;
                              (
                                form.elements.namedItem(
                                  "upstream_project_id",
                                ) as HTMLInputElement
                              ).value = k.upstream_project_id || "";
                            }}
                          >
                            {k.name}
                            <code>{k.id}</code>
                            <small>
                              {k.redacted_value} · {k.upstream_project_name}
                            </small>
                          </button>
                        ))}
                      </div>
                    )}
                  </>
                )}
                {page === "gateway_keys" && (
                  <>
                    <label>
                      {t("models")}
                      <input
                        name="models"
                        required
                        defaultValue={modal.models?.join(", ") || "*"}
                        list="models"
                      />
                      <datalist id="models">
                        {models.map((m) => (
                          <option key={m.model}>{m.model}</option>
                        ))}
                      </datalist>
                      <small>{t("modelHint")}</small>
                    </label>
                    <label>
                      {t("cidrs")}
                      <textarea
                        name="cidrs"
                        rows={4}
                        required
                        defaultValue={modal.cidrs?.join("\n") || ""}
                      />
                      <small>{t("cidrHint")}</small>
                    </label>
                    <label>
                      {t("expires_at")}
                      <input
                        name="expires_at"
                        type="datetime-local"
                        defaultValue={
                          modal.expires_at
                            ? new Date(
                                new Date(modal.expires_at).getTime() -
                                  new Date().getTimezoneOffset() * 60000,
                              )
                                .toISOString()
                                .slice(0, 16)
                            : ""
                        }
                      />
                      <small>{t("expiresHint")}</small>
                    </label>
                  </>
                )}
                <label>
                  {t("status")}
                  <select
                    name="enabled"
                    defaultValue={String(modal.enabled !== false)}
                  >
                    <option value="true">{t("enabled")}</option>
                    <option value="false">{t("disabled")}</option>
                  </select>
                </label>
              </div>
              <div className="modal-footer">
                <button
                  className="secondary"
                  type="button"
                  onClick={() => setModal(null)}
                >
                  {t("cancel")}
                </button>
                <button className="primary" disabled={busy}>
                  {t(busy ? "loading" : "save")}
                </button>
              </div>
            </form>
          </section>
        </div>
      )}
      {secret && (
        <div className="modal-overlay">
          <section
            className="modal secret-modal"
            role="dialog"
            aria-modal="true"
          >
            <div className="resource-icon">
              <KeyRound size={27} />
            </div>
            <h2>{t("newKey")}</h2>
            <p>{t("keyOnce")}</p>
            <code className="new-secret">{secret}</code>
            <button
              className="primary"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(secret);
                  setCopied(true);
                } catch {
                  setCopied(false);
                }
              }}
            >
              {copied ? <Check size={17} /> : <Copy size={17} />}{" "}
              {t(copied ? "copied" : "copy")}
            </button>
            <button className="secondary" onClick={() => setSecret("")}>
              {t("close")}
            </button>
          </section>
        </div>
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
