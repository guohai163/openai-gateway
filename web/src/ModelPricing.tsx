import { useEffect, useState } from "react";
import {
  AlertCircle,
  Check,
  ChevronLeft,
  ChevronRight,
  ExternalLink,
  RefreshCw,
  Search,
} from "lucide-react";

type Row = Record<string, any>;
type Props = {
  api: (path: string, options?: RequestInit) => Promise<any>;
  t: (key: string) => string;
  locale: string;
  onUnauthorized: () => void;
};
export function ModelPricing({ api, t, locale, onUnauthorized }: Props) {
  const [result, setResult] = useState<Row>({
    data: [],
    total: 0,
    missing_count: 0,
    sync: null,
  });
  const [search, setSearch] = useState(""),
    [missing, setMissing] = useState(false),
    [offset, setOffset] = useState(0),
    [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(false),
    [syncing, setSyncing] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const platform = "openai",
    limit = 100;
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const q = new URLSearchParams({
      platform,
      q: search,
      missing: String(missing),
      offset: String(offset),
      limit: String(limit),
    });
    api("pricing?" + q)
      .then((v) => {
        if (!cancelled) setResult(v);
      })
      .catch((e) => {
        if (!cancelled) {
          setError(e.message);
          if (e.message === "unauthorized") onUnauthorized();
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [search, missing, offset, revision]);
  async function sync() {
    setSyncing(true);
    setError("");
    setNotice("");
    try {
      const v = await api("pricing/sync?platform=" + platform, {
        method: "POST",
      });
      setNotice(t("pricingSynced").replace("{count}", String(v.count)));
    } catch (e) {
      const code = e instanceof Error ? e.message : "unexpected_error";
      setError(code);
      if (code === "unauthorized") onUnauthorized();
    } finally {
      setSyncing(false);
      setRevision((n) => n + 1);
    }
  }
  const number = (n: any) =>
    new Intl.NumberFormat(locale, { maximumFractionDigits: 10 }).format(
      Number(n),
    );
  const money = (n: any) =>
    n === null || n === undefined ? "—" : "$" + number(n);
  const date = (v: any) =>
    v
      ? new Intl.DateTimeFormat(locale, {
          dateStyle: "medium",
          timeStyle: "short",
        }).format(new Date(v))
      : "—";
  function priceLines(
    row: Row,
    field: "input_price" | "output_price" | "cached_input_price",
  ) {
    const modalities = row.modality_prices || {};
    const names = ["text", "image"].filter((name) => modalities[name]);
    if (names.length)
      return (
        <div className="price-rate-stack">
          {names.map((name) => (
            <div className="price-rate-entry" key={name}>
              <small>
                {t(name === "image" ? "imageTokens" : "textTokens")}
              </small>
              <strong>
                {field === "output_price" && !modalities[name].output_applicable
                  ? t("notApplicable")
                  : money(modalities[name][field])}
              </strong>
            </div>
          ))}
        </div>
      );
    const tiered = row.long_context_policy === "tiered";
    return (
      <div className="price-rate-stack">
        <div className="price-rate-entry">
          <small>
            {tiered
              ? t("shortContextUpTo").replace(
                  "{tokens}",
                  number(row.long_context_threshold),
                )
              : t("standardRate")}
          </small>
          <strong>
            {field === "output_price" && !row.output_applicable
              ? t("notApplicable")
              : money(row[field])}
          </strong>
        </div>
        {tiered && (
          <div className="price-rate-entry price-rate-long">
            <small>
              {t("longContextFrom").replace(
                "{tokens}",
                number(row.long_context_threshold),
              )}
            </small>
            <strong>{money(row["long_" + field])}</strong>
          </div>
        )}
      </div>
    );
  }
  const rows = result.data as Row[];
  return (
    <div className="pricing-page">
      <section className="panel pricing-summary">
        <div>
          <h2>{t("pricingScope")}</h2>
          <p>{t("pricingDescription")}</p>
          <small>{t("pricingThresholdHint")}</small>
        </div>
        <div className="heading-actions">
          <button
            className="secondary"
            disabled={loading || syncing}
            onClick={() => {
              setError("");
              setRevision((n) => n + 1);
            }}
          >
            <RefreshCw size={16} />
            {t("refresh")}
          </button>
          <button
            className="primary"
            disabled={syncing}
            onClick={() => void sync()}
          >
            <RefreshCw size={16} className={syncing ? "spinning" : ""} />
            {t(syncing ? "pricingSyncing" : "syncPricing")}
          </button>
        </div>
      </section>
      <div className="pricing-meta">
        <span>
          {t("lastPriceSync")}: {date(result.sync?.succeeded_at)}
        </span>
        <span className={result.missing_count ? "missing-price-text" : ""}>
          {t("missingPrices")}: {result.missing_count}
        </span>
        <a
          href="https://developers.openai.com/api/docs/pricing"
          target="_blank"
          rel="noreferrer"
        >
          {t("pricingSource")}
          <ExternalLink size={13} />
        </a>
      </div>
      {(error || result.sync?.last_error) && (
        <div className="alert" role="alert">
          <AlertCircle size={17} />
          {t(error || result.sync.last_error)}
        </div>
      )}
      {notice && (
        <div className="notice" role="status">
          <Check size={16} />
          {notice}
        </div>
      )}
      <section className="panel data-panel">
        <div className="table-toolbar">
          <label className="pricing-platform">
            {t("platform")}
            <select
              aria-label={t("platform")}
              value={platform}
              onChange={() => {}}
            >
              <option value="openai">OpenAI</option>
            </select>
          </label>
          <label className="search">
            <Search size={16} />
            <input
              aria-label={t("searchPricing")}
              placeholder={t("searchPricing")}
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setOffset(0);
              }}
            />
          </label>
          <label className="pricing-missing-filter">
            <input
              type="checkbox"
              checked={missing}
              onChange={(e) => {
                setMissing(e.target.checked);
                setOffset(0);
              }}
            />
            {t("onlyMissingPrices")}
          </label>
          <span className="record-count">
            {result.total} {t("total")}
          </span>
        </div>
        <div className="table-scroll">
          <table className="pricing-table">
            <thead>
              <tr>
                {["model", "inputPrice", "outputPrice", "cachedInputPrice"].map(
                  (column) => (
                    <th key={column} scope="col">
                      {t(column)}
                    </th>
                  ),
                )}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.platform + ":" + row.model}
                  className={
                    row.price_status !== "ready" ? "missing-price-row" : ""
                  }
                >
                  <td className="pricing-model-cell">
                    <div className="pricing-model-heading">
                      <code>{row.model}</code>
                      <span
                        className={
                          "badge " +
                          (row.price_status === "ready" ? "active" : "invalid")
                        }
                      >
                        <i />
                        {t("price_" + row.price_status)}
                      </span>
                    </div>
                    <div className="pricing-model-meta">
                      <span>
                        {row.platform === "openai" ? "OpenAI" : row.platform}
                      </span>
                      <span>
                        {t("inModelCatalog")}:{" "}
                        {t(row.in_catalog ? "yes" : "no")}
                      </span>
                    </div>
                    <p className="pricing-context-rule">
                      {Object.keys(row.modality_prices || {}).length
                        ? t("imagePricingHint")
                        : row.long_context_policy === "tiered"
                          ? t(
                              row.long_context_surcharge
                                ? "longContextAbove"
                                : "longContextUnchanged",
                            ).replace(
                              "{tokens}",
                              number(row.long_context_threshold),
                            )
                          : t(
                              row.long_context_policy === "none"
                                ? "longContextNone"
                                : "longContextUnknown",
                            )}
                    </p>
                    <small className="pricing-model-date">
                      {t("syncedPriceAt")}: {date(row.synced_at)}
                    </small>
                  </td>
                  <td data-label={t("inputPrice")}>
                    {priceLines(row, "input_price")}
                  </td>
                  <td data-label={t("outputPrice")}>
                    {priceLines(row, "output_price")}
                  </td>
                  <td data-label={t("cachedInputPrice")}>
                    {priceLines(row, "cached_input_price")}
                    {row.cache_write_30m_price != null && (
                      <details className="price-write-details">
                        <summary>{t("cacheWrites")}</summary>
                        <small>
                          {t("cacheWrite30m")}:{" "}
                          {money(row.cache_write_30m_price)}
                        </small>
                        <small>
                          {t("cacheWrite12h")}:{" "}
                          {money(row.cache_write_12h_price)}
                        </small>
                        {row.long_context_policy === "tiered" && (
                          <>
                            <small>
                              {t("longContextRate")} · {t("cacheWrite30m")}:{" "}
                              {money(row.long_cache_write_30m_price)}
                            </small>
                            <small>
                              {t("longContextRate")} · {t("cacheWrite12h")}:{" "}
                              {money(row.long_cache_write_12h_price)}
                            </small>
                          </>
                        )}
                      </details>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!rows.length && (
            <div className="empty-state">
              <h3>{t(loading ? "loading" : "noPricing")}</h3>
              <p>{t("noPricingHint")}</p>
            </div>
          )}
        </div>
        <footer className="table-footer">
          <span>
            {t("page")} {number(offset / limit + 1)}
          </span>
          <div>
            <button
              disabled={!offset || loading}
              onClick={() => setOffset((n) => n - limit)}
            >
              <ChevronLeft size={16} />
              {t("previous")}
            </button>
            <button
              disabled={offset + limit >= result.total || loading}
              onClick={() => setOffset((n) => n + limit)}
            >
              {t("next")}
              <ChevronRight size={16} />
            </button>
          </div>
        </footer>
      </section>
    </div>
  );
}
