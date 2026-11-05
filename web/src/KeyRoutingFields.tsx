import { useEffect, useState } from "react";
import { formatDateTime } from "./date-time.mjs";

type Upstream = {
  id: string;
  name: string;
  enabled: boolean;
  invalid: boolean;
  quota_blocked: boolean;
};

export function KeyRoutingFields({
  row,
  api,
  t,
}: {
  row: Record<string, any>;
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
}) {
  const [mode, setMode] = useState<string>(row.routing_mode || "round_robin");
  const [ids, setIDs] = useState<string[]>(row.upstream_key_ids || []);
  const [all, setAll] = useState(!row.upstream_key_ids?.length);
  const [upstreams, setUpstreams] = useState<Upstream[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30000);
    return () => window.clearInterval(timer);
  }, []);
  const savedSchedule =
    mode === "primary_backup" &&
    row.routing_mode === mode &&
    JSON.stringify(ids) === JSON.stringify(row.upstream_key_ids) &&
    row.routing_started_at;
  const anchor = savedSchedule
    ? new Date(row.routing_started_at).getTime()
    : now;
  const interval = ids.length ? 86400000 / ids.length : 86400000;
  const slot = Math.floor(Math.max(0, now - anchor) / interval);
  const primaryID = ids[slot % Math.max(1, ids.length)];
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    (async () => {
      const rows: Upstream[] = [];
      for (let offset = 0; ; offset += 500) {
        const result = await api(`upstream_keys?limit=500&offset=${offset}`);
        if (!active) return;
        rows.push(...result.data);
        if (result.data.length < 500) break;
      }
      if (active) setUpstreams(rows);
    })()
      .catch((e) => {
        if (active)
          setError(e instanceof Error ? e.message : "unexpected_error");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [api, attempt]);
  const move = (index: number, delta: number) => {
    const next = [...ids];
    [next[index], next[index + delta]] = [next[index + delta], next[index]];
    setIDs(next);
  };
  return (
    <fieldset className="key-routing-fields">
      <legend>{t("keyRouting")}</legend>
      <label>
        {t("routingMode")}
        <select
          name="routing_mode"
          value={mode}
          onChange={(e) => {
            setMode(e.target.value);
            if (e.target.value === "primary_backup") setAll(false);
          }}
        >
          <option value="round_robin">{t("routingRoundRobin")}</option>
          <option value="primary_backup">{t("routingPrimaryBackup")}</option>
        </select>
      </label>
      <p>
        {t(
          mode === "primary_backup"
            ? "routingPrimaryHint"
            : "routingRoundRobinHint"
        )}
      </p>
      {mode === "round_robin" && (
        <label className="routing-check">
          <input
            type="checkbox"
            checked={all}
            onChange={(e) => setAll(e.target.checked)}
          />
          {t("routingAllUpstreams")}
        </label>
      )}
      <input type="hidden" name="routing_all" value={all ? "true" : "false"} />
      {loading ? (
        <p role="status">{t("loading")}</p>
      ) : error ? (
        <p className="error-hint" role="alert">
          {t(error)}{" "}
          <button
            type="button"
            className="text-button"
            onClick={() => setAttempt(attempt + 1)}
          >
            {t("archiveRetry")}
          </button>
        </p>
      ) : (
        <>
          {!all && (
            <div className="routing-upstream-options">
              {upstreams.map((upstream) => (
                <label key={upstream.id} className="routing-check">
                  <input
                    type="checkbox"
                    checked={ids.includes(upstream.id)}
                    onChange={(e) =>
                      setIDs(
                        e.target.checked
                          ? [...ids, upstream.id]
                          : ids.filter((id) => id !== upstream.id)
                      )
                    }
                  />
                  {upstream.name}{" "}
                  <small>
                    {!upstream.enabled
                      ? t("disabled")
                      : upstream.invalid
                      ? t("invalid")
                      : upstream.quota_blocked
                      ? t("blocked")
                      : ""}
                  </small>
                </label>
              ))}
            </div>
          )}
          {mode === "primary_backup" && ids.length > 0 && (
            <>
              <p>
                {t("routingInterval").replace(
                  "{hours}",
                  new Intl.NumberFormat(undefined, {
                    maximumFractionDigits: 2,
                  }).format(24 / ids.length)
                )}
              </p>
              <ol className="routing-order">
                {ids.map((id, index) => (
                  <li key={id}>
                    <span>
                      {index + 1}.{" "}
                      {upstreams.find((item) => item.id === id)?.name || id}
                    </span>
                    <button
                      type="button"
                      className="secondary"
                      disabled={index === 0}
                      aria-label={`${t("routingMoveUp")} ${index + 1}`}
                      onClick={() => move(index, -1)}
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      className="secondary"
                      disabled={index === ids.length - 1}
                      aria-label={`${t("routingMoveDown")} ${index + 1}`}
                      onClick={() => move(index, 1)}
                    >
                      ↓
                    </button>
                  </li>
                ))}
              </ol>
              {savedSchedule && (
                <p>
                  {t("routingScheduledPrimary")}:{" "}
                  {upstreams.find((item) => item.id === primaryID)?.name ||
                    primaryID}
                  <br />
                  {t("routingNextSwitch")}:{" "}
                  {formatDateTime(anchor + (slot + 1) * interval)}
                </p>
              )}
            </>
          )}
        </>
      )}
      {!all &&
        ids.map((id) => (
          <input type="hidden" key={id} name="upstream_key_ids" value={id} />
        ))}
      <input
        type="hidden"
        name="routing_ready"
        value={error || loading ? "false" : "true"}
      />
      <small>{t("routingSaveHint")}</small>
    </fieldset>
  );
}
