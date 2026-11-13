import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { formatDateTime as date } from "./date-time.mjs";
import {
  quotaCountdown,
  quotaPercent,
  quotaWindowState,
} from "./oauth-usage.mjs";

type Window = {
  remaining_percent: number;
  used_percent: number;
  resets_at: string | null;
};
type Usage = {
  five_hour: Window | null;
  seven_day: Window | null;
  checked_at: string | null;
  next_check_at: string | null;
  error: string;
};
type Props = {
  accountId: string;
  status: string;
  refreshedAt: string | null;
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  locale: string;
  onUnauthorized: () => void;
};

export function OAuthAccountUsage({
  accountId,
  status,
  refreshedAt,
  api,
  t,
  locale,
  onUnauthorized,
}: Props) {
  const [usage, setUsage] = useState<Usage | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  const [reload, setReload] = useState(0);
  const enabled = status !== "disconnected";

  useEffect(() => {
    if (!enabled) {
      setUsage(null);
      setError("");
      return;
    }
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      setBusy(true);
      try {
        const result = await api(
          `oauth_accounts/${encodeURIComponent(accountId)}/usage`,
        );
        if (cancelled) return;
        setUsage(result);
        setError("");
      } catch (e) {
        if (cancelled) return;
        const code = e instanceof Error ? e.message : "unexpected_error";
        setError(code);
        if (code === "unauthorized") onUnauthorized();
      } finally {
        if (!cancelled) {
          setBusy(false);
          setNow(Date.now());
          timer = setTimeout(load, 60000);
        }
      }
    }
    void load();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [accountId, enabled, refreshedAt, reload]);

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(timer);
  }, []);

  if (!enabled)
    return (
      <small className="oauth-usage-message muted">
        {t("oauthUsageAfterConnect")}
      </small>
    );
  const failure = error || usage?.error;
  const windows = [
    ["5h", usage?.five_hour],
    ["7d", usage?.seven_day],
  ] as const;
  const present = windows.filter(([, window]) => quotaWindowState(window, now));
  return (
    <div className={`oauth-account-usage ${failure ? "is-stale" : ""}`}>
      {present.map(([label, window]) => {
        const state = quotaWindowState(window, now)!;
        const text = state.expired
          ? "—"
          : quotaPercent(state.remaining, locale);
        const title = state.expired
          ? t("oauthUsageAwaitingRefresh")
          : `${t("oauthUsageRemaining")} ${text}${state.reset ? ` · ${t("oauthUsageResetsAt")} ${date(state.reset)}` : ""}${usage?.checked_at ? ` · ${t("oauthUsageUpdatedAt")} ${date(usage.checked_at)}` : ""}`;
        return (
          <div
            key={label}
            className={`oauth-usage-row ${label === "7d" ? "weekly" : "short"} ${state.expired ? "is-expired" : ""}`}
            title={title}
          >
            <span className="oauth-usage-label">{label}</span>
            <span
              className="oauth-usage-track"
              role="progressbar"
              aria-label={`${label} ${t("oauthUsageRemaining")}`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={state.expired ? undefined : state.remaining}
              aria-valuetext={
                state.expired ? t("oauthUsageAwaitingRefresh") : text
              }
            >
              {!state.expired && (
                <span style={{ width: `${state.remaining}%` }} />
              )}
            </span>
            <span className="oauth-usage-percent">{text}</span>
            <span className="oauth-usage-reset">
              {state.expired
                ? t("oauthUsageAwaitingRefresh")
                : quotaCountdown(state.reset, now)}
            </span>
          </div>
        );
      })}
      <div className="oauth-usage-footer">
        <small
          className="oauth-usage-message muted"
          title={
            failure
              ? t(failure)
              : usage?.checked_at
                ? `${t("oauthUsageUpdatedAt")} ${date(usage.checked_at)}`
                : undefined
          }
        >
          {failure
            ? t(present.length ? "oauthUsageStale" : "oauthUsageUnavailable")
            : !present.length
              ? t(busy ? "loading" : "oauthUsageNoWindows")
              : t("oauthUsageRemaining")}
        </small>
        <button
          type="button"
          className="text-button oauth-usage-refresh"
          disabled={busy}
          title={t("oauthUsageRefresh")}
          aria-label={t("oauthUsageRefresh")}
          onClick={() => setReload((value) => value + 1)}
        >
          <RefreshCw size={12} className={busy ? "spinning" : ""} />
        </button>
      </div>
      {failure && (
        <small className="oauth-usage-message muted">
          {t(failure)}
          {failure === "oauth_usage_not_supported" && (
            <>
              {" "}
              <a
                href="https://chatgpt.com/settings/usage"
                target="_blank"
                rel="noreferrer noopener"
              >
                {t("oauthUsageView")}
              </a>
            </>
          )}
          {usage?.next_check_at && (
            <span className="oauth-usage-retry">
              {t("oauthUsageNextCheck")}: {date(usage.next_check_at)}
            </span>
          )}
        </small>
      )}
    </div>
  );
}
