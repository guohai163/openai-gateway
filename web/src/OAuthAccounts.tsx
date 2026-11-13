import { useEffect, useRef, useState } from "react";
import { ExternalLink, RefreshCw } from "lucide-react";
import { formatDateTime as date } from "./date-time.mjs";
import { OAuthAccountTest } from "./OAuthAccountTest";

type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  onUnauthorized: () => void;
};
type Account = {
  id: string;
  name: string;
  client_id: string;
  email: string;
  status: string;
  expires_at: string | null;
  next_refresh_at: string | null;
  last_refreshed_at: string | null;
  refresh_error: string;
  model_sync_error: string;
  model_count: number;
};
type Attempt = {
  attempt_id: string;
  account_id: string;
  authorization_url: string;
  expires_at: string;
  redirect_uri: string;
};

export function OAuthAccounts({ api, t, onUnauthorized }: Props) {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [name, setName] = useState("");
  const [pending, setPending] = useState<Attempt | null>(null);
  const [callback, setCallback] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState<Account | null>(null);
  const [catalog, setCatalog] = useState<any>(null);
  const [testing, setTesting] = useState<Account | null>(null);
  const mounted = useRef(true);
  const modelGeneration = useRef(0);

  function fail(e: unknown) {
    if (!mounted.current) return;
    const code = e instanceof Error ? e.message : "unexpected_error";
    setError(code);
    if (code === "unauthorized") onUnauthorized();
  }
  async function load() {
    const result = await api("oauth_accounts");
    if (mounted.current) setAccounts(result.data);
  }
  useEffect(() => {
    mounted.current = true;
    void load().catch(fail);
    return () => {
      mounted.current = false;
      modelGeneration.current++;
    };
  }, []);

  useEffect(() => {
    if (!pending) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      try {
        const result = await api(
          `oauth_accounts/attempts/${pending!.attempt_id}`,
        );
        if (cancelled || !mounted.current) return;
        if (result.status === "success" || result.status === "failed") {
          setPending(null);
          setCallback("");
          if (result.status === "failed")
            setError(result.error || "oauth_authorization_failed");
          await load();
          return;
        }
      } catch (e) {
        if (!cancelled) fail(e);
      }
      if (!cancelled) timer = setTimeout(poll, 2000);
    }
    timer = setTimeout(poll, 1500);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [pending]);

  async function perform(fn: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      fail(e);
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  async function authorize(account?: Account) {
    await perform(async () => {
      const result = await api("oauth_accounts/authorize", {
        method: "POST",
        body: JSON.stringify(account ? { account_id: account.id } : { name }),
      });
      if (!mounted.current) return;
      setPending(result);
      setCallback("");
      setName("");
      await load();
    });
  }
  async function showModels(account: Account) {
    const generation = ++modelGeneration.current;
    setSelected(account);
    setCatalog(null);
    try {
      const result = await api(`oauth_accounts/${account.id}/models`);
      if (mounted.current && generation === modelGeneration.current)
        setCatalog(result);
    } catch (e) {
      if (generation === modelGeneration.current) fail(e);
    }
  }
  return (
    <div className="oauth-page">
      {error && (
        <div className="alert" role="alert">
          {t(error)}
        </div>
      )}
      <section className="panel account-panel">
        <div className="panel-heading">
          <h2>{t("oauthAddAccount")}</h2>
          <button
            className="secondary"
            disabled={busy}
            onClick={() => void perform(load)}
          >
            <RefreshCw size={15} />
            {t("refresh")}
          </button>
        </div>
        <p className="muted">{t("oauthRefreshHint")}</p>
        <form
          className="spending-range"
          onSubmit={(e) => {
            e.preventDefault();
            void authorize();
          }}
        >
          <label>
            {t("name")}
            <input
              required
              maxLength={120}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("oauthNamePlaceholder")}
            />
          </label>
          <button className="primary" disabled={busy || !!pending}>
            {t("oauthStart")}
          </button>
        </form>
      </section>
      {pending && (
        <section
          className="panel account-panel oauth-pending"
          aria-live="polite"
        >
          <h2>{t("oauthWaiting")}</h2>
          <p>{t("oauthOpenHint")}</p>
          <a
            className="oauth-continue"
            href={pending.authorization_url}
            target="_blank"
            rel="noreferrer noopener"
          >
            <img src="/openai-icon.png" alt="" />
            {t("oauthContinue")}
            <ExternalLink size={16} />
          </a>
          <small className="muted">
            {t("expires_at")}: {date(pending.expires_at)}
          </small>
          <details>
            <summary>{t("oauthRemote")}</summary>
            <p className="muted">{t("oauthRemoteHint")}</p>
            <code>{pending.redirect_uri}</code>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void perform(async () => {
                  await api("oauth_accounts/callback", {
                    method: "POST",
                    body: JSON.stringify({ callback_url: callback }),
                  });
                  if (!mounted.current) return;
                  setPending(null);
                  setCallback("");
                  await load();
                });
              }}
            >
              <label>
                {t("oauthCallbackURL")}
                <textarea
                  required
                  rows={3}
                  value={callback}
                  onChange={(e) => setCallback(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
              </label>
              <button className="primary" disabled={busy}>
                {t("oauthComplete")}
              </button>
            </form>
          </details>
          <button
            className="text-button"
            disabled={busy}
            onClick={() => {
              setPending(null);
              setCallback("");
            }}
          >
            {t("close")}
          </button>
        </section>
      )}
      <section className="data-panel">
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  "name",
                  "status",
                  "expires_at",
                  "oauthNextRefresh",
                  "models",
                  "actions",
                ].map((k) => (
                  <th key={k}>{t(k)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {accounts.map((account) => (
                <tr key={account.id}>
                  <td>
                    <div className="resource-name">
                      <strong>{account.name}</strong>
                      <small>{account.email || "—"}</small>
                      <code className="oauth-client-id">
                        {account.client_id || t("pending")}
                      </code>
                    </div>
                  </td>
                  <td>
                    <span
                      className={`badge ${account.status === "connected" ? "active" : "pending"}`}
                    >
                      <i />
                      {t(`oauth_${account.status}`)}
                    </span>
                    {account.refresh_error && (
                      <small className="oauth-error">
                        {t(account.refresh_error)}
                      </small>
                    )}
                  </td>
                  <td>{date(account.expires_at)}</td>
                  <td>
                    {date(account.next_refresh_at)}
                    <small className="oauth-last-refresh">
                      {t("oauthLastRefresh")}: {date(account.last_refreshed_at)}
                    </small>
                  </td>
                  <td>
                    <button
                      className="text-button"
                      disabled={busy}
                      onClick={() => void showModels(account)}
                    >
                      {account.model_count} {t("modelCount")}
                    </button>
                    {account.model_sync_error && (
                      <small className="oauth-error">
                        {t(account.model_sync_error)}
                      </small>
                    )}
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        disabled={busy || account.status !== "connected"}
                        onClick={() => setTesting(account)}
                      >
                        {t("oauthTest")}
                      </button>
                      <button
                        disabled={busy || !!pending}
                        onClick={() => void authorize(account)}
                      >
                        {t("oauthReauthorize")}
                      </button>
                      <button
                        disabled={busy || account.status !== "connected"}
                        onClick={() =>
                          void perform(async () => {
                            await api(
                              `oauth_accounts/${account.id}/sync_models`,
                              { method: "POST", body: "{}" },
                            );
                            await load();
                            if (selected?.id === account.id)
                              await showModels(account);
                          })
                        }
                      >
                        {t("oauthSyncModels")}
                      </button>
                      <button
                        disabled={
                          busy ||
                          !account.next_refresh_at ||
                          Date.parse(account.next_refresh_at) > Date.now()
                        }
                        onClick={() =>
                          void perform(async () => {
                            try {
                              await api(
                                `oauth_accounts/${account.id}/refresh`,
                                { method: "POST", body: "{}" },
                              );
                            } finally {
                              await load();
                            }
                          })
                        }
                      >
                        {t("oauthRefreshToken")}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!accounts.length && (
          <p className="oauth-empty muted">{t("oauthNoAccounts")}</p>
        )}
      </section>
      {testing && (
        <OAuthAccountTest
          key={testing.id}
          account={testing}
          api={api}
          t={t}
          onUnauthorized={onUnauthorized}
          onClose={() => setTesting(null)}
        />
      )}
      {selected && (
        <section className="panel account-panel">
          <div className="panel-heading">
            <h2>
              {selected.name} · {t("models")}
            </h2>
            <button
              onClick={() => {
                modelGeneration.current++;
                setSelected(null);
                setCatalog(null);
              }}
            >
              {t("close")}
            </button>
          </div>
          {catalog ? (
            <>
              <small className="muted">
                {t("model_synced_at")}: {date(catalog.synced_at)}
              </small>
              {catalog.error && (
                <p className="oauth-error">{t(catalog.error)}</p>
              )}
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>{t("name")}</th>
                      <th>Model ID</th>
                    </tr>
                  </thead>
                  <tbody>
                    {catalog.models.map((model: any) => (
                      <tr key={model.slug}>
                        <td>{model.display_name}</td>
                        <td>
                          <code>{model.slug}</code>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {!catalog.models.length && (
                <p className="muted">{t("oauthNoModels")}</p>
              )}
            </>
          ) : (
            <p className="muted">{t("loading")}</p>
          )}
        </section>
      )}
    </div>
  );
}
