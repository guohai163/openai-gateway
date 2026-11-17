import { useEffect, useRef, useState } from "react";
import { Plus, RefreshCw, X } from "lucide-react";
import { formatDateTime } from "./date-time.mjs";

type Row = Record<string, any>;
type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  onUnauthorized: () => void;
};

function localDate(value: string) {
  if (!value) return "";
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}

export function ProxySettings({ api, t, onUnauthorized }: Props) {
  const [rows, setRows] = useState<Row[]>([]),
    [modal, setModal] = useState<Row | null>(null);
  const [target, setTarget] = useState<Row | null>(null),
    [busy, setBusy] = useState(false);
  const [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const mounted = useRef(true);
  function fail(e: unknown) {
    if (!mounted.current) return;
    const code = e instanceof Error ? e.message : "unexpected_error";
    setError(code);
    if (code === "unauthorized") onUnauthorized();
  }
  async function load() {
    const result = await api("proxies");
    if (mounted.current) setRows(result);
  }
  useEffect(() => {
    mounted.current = true;
    void load().catch(fail);
    return () => {
      mounted.current = false;
    };
  }, []);
  async function perform(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (e) {
      fail(e);
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  return (
    <div className="oauth-page">
      {error && (
        <div className="alert" role="alert">
          {t(error)}
        </div>
      )}
      {notice && (
        <div className="notice" role="status">
          {t(notice)}
        </div>
      )}
      <section className="panel account-panel">
        <div className="panel-heading">
          <h2>{t("proxies")}</h2>
          <div className="row-actions">
            <button
              className="secondary"
              disabled={busy}
              onClick={() => void perform(load)}
            >
              <RefreshCw size={16} />
              {t("refresh")}
            </button>
            <button
              className="primary"
              disabled={busy}
              onClick={() => {
                setError("");
                setModal({});
              }}
            >
              <Plus size={16} />
              {t("proxyCreate")}
            </button>
          </div>
        </div>
        <p className="muted">{t("proxiesHint")}</p>
      </section>
      <section className="data-panel">
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  "name",
                  "proxyProtocol",
                  "proxyAddress",
                  "proxyAuth",
                  "status",
                  "expires_at",
                  "proxyUsage",
                  "actions",
                ].map((field) => (
                  <th key={field}>{t(field)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}>
                  <td>{row.name}</td>
                  <td>{row.protocol.toUpperCase()}</td>
                  <td>
                    <code>
                      {row.host.includes(":") ? `[${row.host}]` : row.host}:
                      {row.port}
                    </code>
                  </td>
                  <td>
                    {row.username || "—"}
                    {row.has_password && (
                      <small className="routing-summary">
                        {t("proxyPasswordStored")}
                      </small>
                    )}
                  </td>
                  <td>
                    <span
                      className={`badge ${row.status === "active" ? "active" : "pending"}`}
                    >
                      <i />
                      {t(row.status)}
                    </span>
                  </td>
                  <td>
                    {row.expires_at
                      ? formatDateTime(row.expires_at)
                      : t("never")}
                  </td>
                  <td>{row.usage_count}</td>
                  <td>
                    <div className="row-actions">
                      <button
                        disabled={busy}
                        onClick={() => {
                          setError("");
                          setModal(row);
                        }}
                      >
                        {t("edit")}
                      </button>
                      <button
                        disabled={busy || row.usage_count > 0}
                        title={row.usage_count > 0 ? t("proxy_in_use") : ""}
                        onClick={() => setTarget(row)}
                      >
                        {t("delete")}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!rows.length && <p className="oauth-empty muted">{t("proxyEmpty")}</p>}
      </section>
      {modal && (
        <ProxyForm
          key={modal.id || "new"}
          row={modal}
          busy={busy}
          error={error}
          t={t}
          onClose={() => setModal(null)}
          onSave={(input) =>
            void perform(async () => {
              await api(
                `proxies${modal.id ? `/${encodeURIComponent(modal.id)}` : ""}`,
                {
                  method: modal.id ? "PATCH" : "POST",
                  body: JSON.stringify(input),
                },
              );
              if (!mounted.current) return;
              setModal(null);
              setNotice("updated");
              await load();
            })
          }
        />
      )}
      {target && (
        <div className="modal-overlay">
          <section
            className="modal delete-key-modal"
            role="alertdialog"
            aria-modal="true"
            aria-label={t("proxyDelete")}
          >
            <div className="modal-heading">
              <h2>{t("proxyDelete")}</h2>
            </div>
            <div className="delete-key-body">
              <strong>{target.name}</strong>
              <p>{t("proxyDeleteHint")}</p>
              {error && <p className="error-hint">{t(error)}</p>}
            </div>
            <div className="modal-footer">
              <button
                disabled={busy}
                className="secondary"
                onClick={() => setTarget(null)}
              >
                {t("cancel")}
              </button>
              <button
                className="danger-button"
                disabled={busy}
                onClick={() =>
                  void perform(async () => {
                    await api(`proxies/${encodeURIComponent(target.id)}`, {
                      method: "DELETE",
                    });
                    if (!mounted.current) return;
                    setTarget(null);
                    await load();
                  })
                }
              >
                {t("delete")}
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}

function ProxyForm({
  row,
  busy,
  error,
  t,
  onClose,
  onSave,
}: {
  row: Row;
  busy: boolean;
  error: string;
  t: Props["t"];
  onClose: () => void;
  onSave: (input: Row) => void;
}) {
  const [expires, setExpires] = useState(localDate(row.expires_at || ""));
  return (
    <div className="modal-overlay">
      <section
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="proxy-form-title"
      >
        <div className="modal-heading">
          <h2 id="proxy-form-title">
            {t(row.id ? "proxyEdit" : "proxyCreate")}
          </h2>
          <button
            type="button"
            disabled={busy}
            onClick={onClose}
            aria-label={t("close")}
          >
            <X size={20} />
          </button>
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const form = new FormData(e.currentTarget);
            const input: Row = Object.fromEntries(form);
            input.port = Number(input.port);
            input.enabled = form.has("enabled");
            input.expires_at = expires ? new Date(expires).toISOString() : null;
            if (row.id && !input.password && !form.has("clear_password"))
              delete input.password;
            if (form.has("clear_password")) input.password = "";
            delete input.clear_password;
            onSave(input);
          }}
        >
          <fieldset className="form-fields proxy-form-fields" disabled={busy}>
            {error && (
              <div className="alert" role="alert">
                {t(error)}
              </div>
            )}
            <label>
              {t("name")}
              <input
                name="name"
                required
                maxLength={120}
                defaultValue={row.name || ""}
              />
            </label>
            <label>
              {t("proxyProtocol")}
              <select name="protocol" defaultValue={row.protocol || "socks5"}>
                <option value="socks5">SOCKS5</option>
                <option value="http">HTTP</option>
                <option value="https">HTTPS</option>
              </select>
            </label>
            <div className="proxy-address-fields">
              <label>
                {t("proxyHost")}
                <input
                  name="host"
                  required
                  maxLength={253}
                  placeholder="proxy.example.com"
                  defaultValue={row.host || ""}
                />
              </label>
              <label>
                {t("proxyPort")}
                <input
                  name="port"
                  required
                  type="number"
                  min="1"
                  max="65535"
                  step="1"
                  defaultValue={row.port || 1080}
                />
              </label>
            </div>
            <label>
              {t("proxyUsername")}
              <input
                name="username"
                autoComplete="off"
                maxLength={255}
                defaultValue={row.username || ""}
              />
            </label>
            <label>
              {t("proxyPassword")}
              <input
                name="password"
                type="password"
                autoComplete="new-password"
                maxLength={255}
                placeholder={row.has_password ? t("proxyKeepPassword") : ""}
              />
            </label>
            {row.has_password && (
              <label className="routing-check">
                <input name="clear_password" type="checkbox" />
                {t("proxyClearPassword")}
              </label>
            )}
            <small className="muted">{t("proxyCredentialsHint")}</small>
            <label>
              {t("expires_at")}
              <input
                type="datetime-local"
                value={expires}
                onChange={(e) => setExpires(e.target.value)}
              />
              <small>{t("proxyExpiryHint")}</small>
            </label>
            <div className="row-actions">
              {[7, 30, 90, 180].map((days) => (
                <button
                  type="button"
                  className="secondary"
                  key={days}
                  onClick={() =>
                    setExpires(
                      localDate(
                        new Date(Date.now() + days * 86400000).toISOString(),
                      ),
                    )
                  }
                >
                  {days} {t("proxyDays")}
                </button>
              ))}
              <button
                type="button"
                className="text-button"
                onClick={() => setExpires("")}
              >
                {t("never")}
              </button>
            </div>
            <label className="routing-check">
              <input
                name="enabled"
                type="checkbox"
                defaultChecked={row.enabled ?? true}
              />
              {t("enabled")}
            </label>
          </fieldset>
          <div className="modal-footer">
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={onClose}
            >
              {t("cancel")}
            </button>
            <button className="primary" disabled={busy}>
              {t("save")}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}
