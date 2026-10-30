import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowUpRight,
  Coins,
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  Database,
  Eye,
  Globe,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Monitor,
  Moon,
  Plus,
  RefreshCw,
  Search,
  Settings,
  ShieldCheck,
  SquareTerminal,
  Sun,
  Terminal,
  X,
} from "lucide-react";
import { messages, resolveLanguage } from "./i18n.mjs";
import { ModelMultiSelect } from "./ModelMultiSelect";
import { KeyUsageDialog } from "./KeyUsageDialog";
import { IPAllowlistField } from "./IPAllowlistField";
import { expiryDayOptions, fetchAllModels } from "./gateway-key-form.mjs";
import {
  readThemePreference,
  resolveTheme,
  themeStorageKey,
} from "./theme.mjs";
import { ModelPricing } from "./ModelPricing";
import { RequestTokens, RequestCost } from "./RequestUsage";
import "./style.css";
type Row = Record<string, any>;
type Page =
  | "overview"
  | "pricing"
  | "upstream_keys"
  | "gateway_keys"
  | "logs"
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
  const [themePreference, setThemePreference] = useState<
    "system" | "light" | "dark"
  >(readThemePreference);
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const update = () => {
      const theme = resolveTheme(themePreference, media.matches);
      document.documentElement.dataset.theme = theme;
      document
        .querySelector('meta[name="theme-color"]')
        ?.setAttribute("content", theme === "dark" ? "#212121" : "#ffffff");
    };
    update();
    media.addEventListener("change", update);
    try {
      localStorage.setItem(themeStorageKey, themePreference);
    } catch {}
    return () => media.removeEventListener("change", update);
  }, [themePreference]);
  const [session, setSession] = useState<string | null | undefined>(),
    [page, setPage] = useState<Page>("overview"),
    [data, setData] = useState<Row[]>([]),
    [dashboard, setDashboard] = useState<Row>({}),
    [settings, setSettings] = useState<Row>({});
  const [models, setModels] = useState<Row[]>([]);
  const [loading, setLoading] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [modal, setModal] = useState<Row | null>(null),
    [catalog, setCatalog] = useState<Row | null>(null),
    [usage, setUsage] = useState<Row | null>(null),
    [deleteTarget, setDeleteTarget] = useState<Row | null>(null),
    [newKeyInfo, setNewKeyInfo] = useState<Row | null>(null),
    [secret, setSecret] = useState(""),
    [copied, setCopied] = useState(false),
    [offset, setOffset] = useState(0),
    [query, setQuery] = useState("");
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
    if (page === "pricing") return;
    setLoading(true);
    setError("");
    try {
      const q = new URLSearchParams({ limit: "100", offset: String(offset) });
      const path =
        page === "overview"
          ? "dashboard"
          : page === "settings"
            ? "settings"
            : page + "?" + q;
      const [catalog, result] = await Promise.all([
        fetchAllModels(api),
        api(path),
      ]);
      setModels(catalog);
      if (page === "overview") setDashboard(result);
      else if (page === "settings") setSettings(result);
      else setData(result.data);
    } catch (e) {
      fail(e);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    if (session) void load();
  }, [session, page, offset]);
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
  async function showCatalog(row: Row) {
    setBusy(true);
    setError("");
    try {
      const result = await api(`upstream_keys/${row.id}/model_catalog`);
      setCatalog({ ...result, name: row.name });
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  }
  async function deletePrivateKey() {
    if (!deleteTarget) return;
    setBusy(true);
    setError("");
    try {
      await api(`gateway_keys/${deleteTarget.id}`, { method: "DELETE" });
      setDeleteTarget(null);
      setNotice("keyDeleted");
      await load();
    } catch (error) {
      fail(error);
    } finally {
      setBusy(false);
    }
  }
  async function save(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!modal) return;
    const form = new FormData(e.currentTarget);
    const input: Row = Object.fromEntries(form);
    input.enabled = input.enabled === "true";
    if (page === "gateway_keys") {
      input.models = form.getAll("models");
      if (!input.models.length) {
        setError("models_required");
        return;
      }
      input.cidrs = input.cidrs
        .split(/[\n,]/)
        .map((s: string) => s.trim())
        .filter(Boolean);
      if (input.expires_in_days)
        input.expires_in_days = Number(input.expires_in_days);
      else delete input.expires_in_days;
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
        setNewKeyInfo({
          id: v.id,
          name: input.name,
          models: input.models || ["*"],
        });
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
  const ThemeIcon =
    themePreference === "system"
      ? Monitor
      : themePreference === "dark"
        ? Moon
        : Sun;
  const appearance = (
    <label className="appearance">
      <ThemeIcon size={16} aria-hidden="true" />
      <select
        aria-label={t("appearance")}
        value={themePreference}
        onChange={(e) =>
          setThemePreference(e.target.value as "system" | "light" | "dark")
        }
      >
        <option value="system">{t("themeSystem")}</option>
        <option value="light">{t("themeLight")}</option>
        <option value="dark">{t("themeDark")}</option>
      </select>
    </label>
  );
  const displayControls = (
    <div className="display-controls">
      {appearance}
      {languages}
    </div>
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
        <SquareTerminal size={21} />
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
        <SquareTerminal />
        {t("loading")}
      </div>
    );
  if (session === null)
    return (
      <div className="login-layout">
        <header className="login-header">
          {mark}
          {displayControls}
        </header>
        <main className="login-main">
          <form className="login-card" onSubmit={login}>
            <h1>{t("loginTitle")}</h1>
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
            </button>
            <span className="login-security">
              <ShieldCheck size={14} />
              {t("secure")}
            </span>
          </form>
        </main>
      </div>
    );
  const nav: [Page, React.ReactNode][] = [
    ["overview", <LayoutDashboard size={18} />],
    ["upstream_keys", <KeyRound size={18} />],
    ["pricing", <Coins size={18} />],
    ["gateway_keys", <ShieldCheck size={18} />],
    ["logs", <Activity size={18} />],
    ["audit", <Terminal size={18} />],
    ["settings", <Settings size={18} />],
  ];
  const canAdd = ["upstream_keys", "gateway_keys"].includes(page);
  const status = (r: Row) =>
    !r.enabled
      ? "disabled"
      : r.invalid
        ? "invalid"
        : r.quota_blocked
          ? "blocked"
          : page === "upstream_keys" && !r.model_synced_at
            ? "pending"
            : "active";
  const columns: Record<string, string[]> = {
    upstream_keys: [
      "name",
      "models",
      "status",
      "model_synced_at",
      "next_probe_at",
    ],
    gateway_keys: ["name", "models", "cidrs", "status", "expires_at"],
    logs: [
      "created_at",
      "model",
      "status",
      "token_usage",
      "request_cost",
      "ip",
      "duration_ms",
      "gateway_name",
      "upstream_name",
      "endpoint",
      "error_code",
      "kind",
    ],
    audit: ["created_at", "action", "resource", "resource_id"],
  };
  function cell(c: string, r: Row) {
    const v = r[c];
    if (c === "token_usage")
      return <RequestTokens row={r} locale={locale} t={t} />;
    if (c === "request_cost")
      return <RequestCost row={r} locale={locale} t={t} />;
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
      ["created_at", "expires_at", "model_synced_at", "next_probe_at"].includes(
        c,
      )
    )
      return c === "expires_at" && !v ? t("never") : date(v);
    if (c === "models" && page === "upstream_keys")
      return (
        <button
          className="text-button"
          disabled={busy}
          onClick={() => void showCatalog(r)}
        >
          {number(v?.length || 0)} {t("modelCount")}
          <ChevronRight size={14} />
        </button>
      );
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
    if (["input_tokens", "output_tokens"].includes(c)) return number(v);
    if (c === "kind") return t(v === "probe" ? "probeKind" : v);
    if (c === "name")
      return (
        <div className="resource-name">
          <strong>{v}</strong>
          <small>{r.prefix ? r.prefix + "…" : ""}</small>
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
        pattern={
          page === "gateway_keys" && n === "name" ? "[a-z0-9\\-]+" : undefined
        }
        maxLength={page === "gateway_keys" && n === "name" ? 120 : undefined}
        title={
          page === "gateway_keys" && n === "name"
            ? t("invalid_key_name")
            : undefined
        }
        autoCapitalize={
          page === "gateway_keys" && n === "name" ? "none" : undefined
        }
        spellCheck={page === "gateway_keys" && n === "name" ? false : undefined}
        autoComplete={type === "password" ? "new-password" : "off"}
      />
      {hint && <small>{t(hint)}</small>}
    </label>
  );
  return (
    <div className="app">
      <aside className="sidebar">
        <button
          className="brand-button"
          aria-label={t("brand")}
          onClick={() => navigate("overview")}
        >
          {mark}
        </button>
        <div className="sidebar-label">{t("workspace")}</div>
        <nav>
          {nav.map(([p, icon]) => (
            <React.Fragment key={p}>
              {p === "audit" && (
                <div className="sidebar-label administration">
                  {t("administration")}
                </div>
              )}
              <button
                className={page === p ? "selected" : ""}
                aria-current={page === p ? "page" : undefined}
                aria-label={t(p)}
                title={t(p)}
                onClick={() => navigate(p)}
              >
                {icon}
                <span>{t(p)}</span>
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
          <span className="breadcrumbs">
            {t("workspace")}
            <span className="slash">/</span>
            <strong>{t(page)}</strong>
          </span>
          {displayControls}
        </header>
        <main className="main">
          <div className="page-heading">
            <div>
              <h1>{t(page)}</h1>
              <p>
                {t(
                  page === "overview"
                    ? "introduction"
                    : page === "settings"
                      ? "settingsHint"
                      : page === "pricing"
                        ? "pricingDescription"
                        : "subtitle",
                )}
              </p>
            </div>
            <div className="heading-actions">
              {page !== "pricing" && (
                <button
                  className="secondary"
                  disabled={loading || busy}
                  onClick={() => void load()}
                >
                  <RefreshCw size={16} className={loading ? "spinning" : ""} />
                  {t("refresh")}
                </button>
              )}
              {canAdd && (
                <button
                  className="primary"
                  onClick={() => {
                    setModal({});
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
          {page === "pricing" ? (
            <ModelPricing
              api={api}
              t={t}
              locale={locale}
              onUnauthorized={() => setSession(null)}
            />
          ) : page === "overview" ? (
            <>
              <div className="stats-grid">
                {[
                  ["available", dashboard.available_keys],
                  ["requests", dashboard.requests_today],
                  [
                    "tokens",
                    Number(dashboard.input_tokens || 0) +
                      Number(dashboard.output_tokens || 0),
                  ],
                  ["blockedScopes", dashboard.blocked_scopes],
                ].map(([label, value]: any) => (
                  <section className="stat-card" key={label}>
                    <div className="stat-label">{t(label)}</div>
                    <strong>{value === null ? "→" : number(value)}</strong>
                    {(label === "available" || label === "tokens") && (
                      <small>
                        {label === "available"
                          ? t("total") + " " + number(dashboard.upstream_keys)
                          : t("input_tokens") +
                            " " +
                            number(dashboard.input_tokens)}
                      </small>
                    )}
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
                    ["upstream_keys", KeyRound, dashboard.upstream_keys],
                    ["pricing", Coins, null],
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
                      <strong>{value === null ? "→" : number(value)}</strong>
                      <ChevronRight size={17} />
                    </button>
                  ))}
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
                    onClick={() => navigate("upstream_keys")}
                  >
                    {t("add")} {t("upstream_keys")}
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
                  {Object.entries(settings).map(([k, v]) => (
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
                <span className="record-count">
                  {number(data.length)} {t("total")}
                </span>
              </div>
              <div className="table-scroll">
                <table
                  className={page === "logs" ? "request-log-table" : undefined}
                >
                  <thead>
                    <tr>
                      {columns[page]?.map((c) => (
                        <th key={c}>{t(c)}</th>
                      ))}
                      {canAdd && (
                        <th className="actions-column">{t("actions")}</th>
                      )}
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
                            <td className="actions-column">
                              <div className="row-actions">
                                <button
                                  onClick={() => {
                                    setModal(r);
                                    setError("");
                                  }}
                                >
                                  {t("edit")}
                                </button>
                                {page === "gateway_keys" && (
                                  <>
                                    <button
                                      type="button"
                                      onClick={() =>
                                        setUsage({
                                          ...r,
                                          initialKey: "",
                                          showKey: true,
                                        })
                                      }
                                    >
                                      <Eye size={14} />
                                      {t("revealKey")}
                                    </button>
                                    <button
                                      type="button"
                                      onClick={() =>
                                        setUsage({ ...r, initialKey: "" })
                                      }
                                    >
                                      {t("howToUse")}
                                    </button>
                                    <button
                                      type="button"
                                      className="danger-link"
                                      disabled={busy}
                                      onClick={() => {
                                        setError("");
                                        setDeleteTarget(r);
                                      }}
                                    >
                                      {t("delete")}
                                    </button>
                                  </>
                                )}
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
            className="modal form-modal"
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
                {field(
                  "name",
                  "text",
                  page === "gateway_keys" ? "keyNameHint" : "",
                  !modal.id || page === "gateway_keys",
                )}
                {page === "upstream_keys" && (
                  <>
                    {!modal.id &&
                      field("key", "password", "upstreamHint", true)}
                    {field("probe_model", "text", "probeHint")}
                  </>
                )}
                {page === "gateway_keys" && (
                  <>
                    <ModelMultiSelect
                      models={models.map((row) => row.model)}
                      defaultValue={modal.models || ["*"]}
                      t={t}
                    />
                    <IPAllowlistField
                      editing={!!modal.id}
                      savedCIDRs={modal.cidrs}
                      request={api}
                      t={t}
                    />
                    <label>
                      {t("expires_in_days")}
                      <select
                        name="expires_in_days"
                        required={!modal.id}
                        defaultValue={modal.id ? "" : "30"}
                      >
                        {modal.id && (
                          <option value="">{t("keepExpiry")}</option>
                        )}
                        {expiryDayOptions.map((days) => (
                          <option key={days} value={days}>
                            {t("expiryDays").replace("{days}", String(days))}
                          </option>
                        ))}
                      </select>
                      <small>{t("expiresHint")}</small>
                      {modal.id && (
                        <small>
                          {t("expires_at")}:{" "}
                          {modal.expires_at
                            ? date(modal.expires_at)
                            : t("never")}
                        </small>
                      )}
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
      {catalog && (
        <div className="modal-overlay">
          <section
            className="modal catalog-modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="catalog-title"
          >
            <div className="modal-heading">
              <div>
                <span className="eyebrow">{catalog.name}</span>
                <h2 id="catalog-title">{t("modelCatalog")}</h2>
              </div>
              <button aria-label={t("close")} onClick={() => setCatalog(null)}>
                <X size={20} />
              </button>
            </div>
            <div className="catalog-body">
              <p>
                {t("model_synced_at")}: {date(catalog.synced_at)}
              </p>
              {catalog.catalog ? (
                <>
                  <p>
                    {number(catalog.catalog.data.length)} {t("modelCount")}
                  </p>
                  <div className="catalog-models">
                    {catalog.catalog.data.map((m: Row) => (
                      <code key={m.id}>{m.id}</code>
                    ))}
                  </div>
                  <details className="catalog-response">
                    <summary>{t("rawModelCatalog")}</summary>
                    <pre>{JSON.stringify(catalog.catalog, null, 2)}</pre>
                  </details>
                </>
              ) : (
                <p>{t("noCatalog")}</p>
              )}
            </div>
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
            <button
              className="secondary"
              onClick={() => {
                setUsage({ ...newKeyInfo, initialKey: secret });
                setSecret("");
                setNewKeyInfo(null);
              }}
            >
              {t("howToUse")}
            </button>
            <button
              className="secondary"
              onClick={() => {
                setSecret("");
                setNewKeyInfo(null);
              }}
            >
              {t("close")}
            </button>
          </section>
        </div>
      )}
      {deleteTarget && (
        <div className="modal-overlay">
          <section
            className="modal delete-key-modal"
            role="alertdialog"
            aria-modal="true"
            aria-labelledby="delete-key-title"
            aria-describedby="delete-key-description"
          >
            <div className="modal-heading">
              <h2 id="delete-key-title">{t("deletePrivateKey")}</h2>
              <button
                type="button"
                aria-label={t("close")}
                disabled={busy}
                onClick={() => setDeleteTarget(null)}
              >
                <X size={20} />
              </button>
            </div>
            <div className="delete-key-body">
              <strong>{deleteTarget.name}</strong>
              <code>{deleteTarget.prefix}…</code>
              <p id="delete-key-description">{t("deleteKeyHint")}</p>
              {alert}
            </div>
            <div className="modal-footer">
              <button
                type="button"
                className="secondary"
                disabled={busy}
                onClick={() => setDeleteTarget(null)}
              >
                {t("cancel")}
              </button>
              <button
                type="button"
                className="danger-button"
                disabled={busy}
                onClick={() => void deletePrivateKey()}
              >
                {t(busy ? "loading" : "confirmDeleteKey")}
              </button>
            </div>
          </section>
        </div>
      )}
      {usage && (
        <KeyUsageDialog
          key={usage.id}
          name={usage.name || t("gateway_keys")}
          allowedModels={usage.models || ["*"]}
          availableModels={models.map((row) => row.model)}
          initialKey={usage.initialKey}
          showKey={usage.showKey}
          loadKey={(original) =>
            api(`gateway_keys/${usage.id}/reveal`, {
              method: "POST",
              body: JSON.stringify(original ? { key: original } : {}),
            })
          }
          onClose={() => setUsage(null)}
          t={t}
        />
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
