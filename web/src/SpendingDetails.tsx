import { useEffect, useState } from "react";
import { ChevronDown, ChevronUp, RefreshCw } from "lucide-react";

type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  locale: string;
  resource?: string;
  resourceId?: string;
  compact?: boolean;
};
export function usd(value: unknown, locale: string) {
  return value == null
    ? "—"
    : new Intl.NumberFormat(locale, {
        style: "currency",
        currency: "USD",
        maximumFractionDigits: 8,
      }).format(Number(value));
}
export function SpendingDetails({
  api,
  t,
  locale,
  resource,
  resourceId,
  compact = false,
}: Props) {
  const [data, setData] = useState<any>(null),
    [expanded, setExpanded] = useState(compact),
    [error, setError] = useState("");
  const [from, setFrom] = useState(""),
    [to, setTo] = useState(""),
    [loading, setLoading] = useState(false);
  const [customRange, setCustomRange] = useState(false);
  const [monthStart, setMonthStart] = useState("");
  const path =
    resource && resourceId ? `${resource}/${resourceId}/spending` : "spending";
  const n = (value: number) => new Intl.NumberFormat(locale).format(value || 0);
  async function load(query = "") {
    setLoading(true);
    setError("");
    try {
      const result = await api(path + query);
      setData(result);
      setCustomRange(!!query);
      const localDate = (value: string) =>
        new Intl.DateTimeFormat("en-CA", {
          timeZone: result.timezone,
          year: "numeric",
          month: "2-digit",
          day: "2-digit",
        }).format(new Date(value));
      setFrom(localDate(result.from));
      if (!query) setMonthStart(localDate(result.from));
      setTo(
        localDate(new Date(new Date(result.to).getTime() - 1).toISOString()),
      );
    } catch (error) {
      setError(error instanceof Error ? error.message : "unexpected_error");
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void load();
  }, [path]);
  const summary = data?.summary;
  const summaryCost =
    summary?.requests === 0
      ? usd(0, locale)
      : summary?.total_cost == null
        ? t("unknown")
        : usd(summary.total_cost, locale);
  return (
    <section className={compact ? "spending-detail" : "panel monthly-spending"}>
      {!compact && (
        <div className="monthly-spending-heading">
          <div>
            <span className="eyebrow">
              {t(customRange ? "selectedRangeSpend" : "calendarMonthSpend")}
            </span>
            <strong>{summaryCost}</strong>
            <small>
              {data?.timezone} · {from} — {to}
              {summary?.unknown_cost_requests > 0
                ? ` · ${t("partialEstimate")}`
                : ""}
            </small>
          </div>
          <button
            type="button"
            className="secondary"
            onClick={() => setExpanded(!expanded)}
          >
            {t(expanded ? "lessDetails" : "moreDetails")}
            {expanded ? <ChevronUp size={15} /> : <ChevronDown size={15} />}
          </button>
        </div>
      )}
      {expanded && (
        <div className="spending-expanded">
          <form
            className="spending-range"
            onSubmit={(event) => {
              event.preventDefault();
              void load("?" + new URLSearchParams({ from, to }));
            }}
          >
            <label>
              {t("dateFrom")}
              <input
                type="date"
                value={from}
                required
                onChange={(event) => setFrom(event.target.value)}
              />
            </label>
            <label>
              {t("dateTo")}
              <input
                type="date"
                value={to}
                min={from}
                required
                onChange={(event) => setTo(event.target.value)}
              />
            </label>
            <button className="secondary" disabled={loading}>
              <RefreshCw size={14} />
              {t("querySpend")}
            </button>
            <button
              className="text-button"
              type="button"
              disabled={loading}
              onClick={() => void load()}
            >
              {t("currentCalendarMonth")}
            </button>
            <button
              className="text-button"
              type="button"
              disabled={loading}
              onClick={() => {
                const [year, month] = (monthStart || from)
                  .split("-")
                  .map(Number);
                if (!year || !month) return;
                const first = new Date(Date.UTC(year, month - 2, 1)),
                  last = new Date(Date.UTC(year, month - 1, 0));
                void load(
                  "?" +
                    new URLSearchParams({
                      from: first.toISOString().slice(0, 10),
                      to: last.toISOString().slice(0, 10),
                    }),
                );
              }}
            >
              {t("previousCalendarMonth")}
            </button>
          </form>
          {error && (
            <p className="error-hint" role="alert">
              {t(error)}
            </p>
          )}
          <small className="spending-note">
            {t("report_timezone")}: {data?.timezone}
          </small>
          <p className="spending-totals">
            {t("totalRequests")}: {n(summary?.requests)} · {t("estimatedCost")}:{" "}
            {summaryCost}
            {summary?.unknown_cost_requests > 0 && (
              <span className="spend-partial">
                {" "}
                ·{" "}
                {t("unknownCostCount").replace(
                  "{count}",
                  n(summary.unknown_cost_requests),
                )}
              </span>
            )}
          </p>
          <div className="table-scroll">
            <table className="spending-models">
              <thead>
                <tr>
                  {[
                    "platform",
                    "model",
                    "totalRequests",
                    "input_tokens",
                    "output_tokens",
                    "inputCost",
                    "outputCost",
                    "cost",
                  ].map((key) => (
                    <th key={key}>{t(key)}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(data?.models || []).map((row: any) => (
                  <tr key={row.platform + ":" + row.model}>
                    <td>{row.platform}</td>
                    <td>
                      <code>{row.model || t("unknown")}</code>
                    </td>
                    <td>{n(row.requests)}</td>
                    <td>{n(row.input_tokens)}</td>
                    <td>{n(row.output_tokens)}</td>
                    <td>{usd(row.input_cost, locale)}</td>
                    <td>{usd(row.output_cost, locale)}</td>
                    <td>
                      {usd(row.total_cost, locale)}
                      {row.unknown_cost_requests > 0 && (
                        <small className="spend-partial">
                          {t("unknownCostCount").replace(
                            "{count}",
                            n(row.unknown_cost_requests),
                          )}
                        </small>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!data?.models?.length && (
            <p className="spending-empty">
              {t(loading ? "loading" : "noSpendInRange")}
            </p>
          )}
          <small className="spending-note">{t("monthlySpendHint")}</small>
        </div>
      )}
      {!expanded && error && <p className="error-hint">{t(error)}</p>}
    </section>
  );
}
